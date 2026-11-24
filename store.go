package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Location 存放一个物资位置，支持父子层级（主卧 / 主卧衣柜 / 第一层）。
type Location struct {
	ID        string `json:"id"`
	ParentID  string `json:"parentId,omitempty"`
	Name      string `json:"name"`
	ImageFile string `json:"imageFile,omitempty"`
	CreatedAt string `json:"createdAt"`
}

// Item 存放一件家庭物资。
type Item struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	LocationID   string   `json:"locationId"`
	LocationName string   `json:"locationName"`
	Quantity     int      `json:"quantity"`
	Barcode      string   `json:"barcode,omitempty"`
	Tags         []string `json:"tags,omitempty"`
	ImageFile    string   `json:"imageFile,omitempty"`
	CreatedAt    string   `json:"createdAt"`
	UpdatedAt    string   `json:"updatedAt"`
}

// User 是登录账号；密码以 bcrypt 哈希存储，绝不落明文。
type User struct {
	ID           string `json:"id"`
	Username     string `json:"username"`
	PasswordHash string `json:"passwordHash"`
	CreatedAt    string `json:"createdAt"`
}

type dbFile struct {
	Locations []Location `json:"locations"`
	Items     []Item     `json:"items"`
	Users     []User     `json:"users,omitempty"`
}

// Store 基于 JSON 文件的持久化存储，读写加锁。
type Store struct {
	mu        sync.RWMutex
	dataDir   string
	dbPath    string
	uploadDir string
	db        dbFile
}

var ErrNotFound = errors.New("not found")
var ErrConflict = errors.New("conflict")

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func now() string { return time.Now().Format(time.RFC3339) }

// normalizeTags 解析逗号/顿号/分号/空格分隔的标签，去空白去重。
func normalizeTags(raw string) []string {
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		switch r {
		case ',', '，', '、', ';', '；', ' ', '\t':
			return true
		}
		return false
	})
	seen := map[string]bool{}
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		key := strings.ToLower(p)
		if !seen[key] {
			seen[key] = true
			out = append(out, p)
		}
	}
	return out
}

func NewStore(dataDir string) (*Store, error) {
	s := &Store{
		dataDir:   dataDir,
		dbPath:    filepath.Join(dataDir, "data.json"),
		uploadDir: filepath.Join(dataDir, "uploads"),
	}
	if err := os.MkdirAll(s.uploadDir, 0o755); err != nil {
		return nil, err
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) load() error {
	raw, err := os.ReadFile(s.dbPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.db = dbFile{Locations: []Location{}, Items: []Item{}}
			return nil
		}
		return err
	}
	if len(strings.TrimSpace(string(raw))) == 0 {
		s.db = dbFile{Locations: []Location{}, Items: []Item{}}
		return nil
	}
	return json.Unmarshal(raw, &s.db)
}

// save 调用方需持有写锁。
func (s *Store) save() error {
	raw, err := json.MarshalIndent(s.db, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.dbPath + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.dbPath)
}

func (s *Store) UploadDir() string { return s.uploadDir }

// ---------- 位置 ----------

func (s *Store) ListLocations() []Location {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Location, len(s.db.Locations))
	copy(out, s.db.Locations)
	sort.Slice(out, func(i, j int) bool {
		if out[i].ParentID != out[j].ParentID {
			return out[i].ParentID < out[j].ParentID
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func (s *Store) locationByIDLocked(id string) (Location, bool) {
	for _, l := range s.db.Locations {
		if l.ID == id {
			return l, true
		}
	}
	return Location{}, false
}

// LocationByID 按 ID 查询位置。
func (s *Store) LocationByID(id string) (Location, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.locationByIDLocked(id)
}

// AddLocation 在指定父位置下创建子位置；parentID 为空则为根位置。同名仅在同级判重。
func (s *Store) AddLocation(name, parentID, imageFile string) (Location, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Location{}, fmt.Errorf("位置名称不能为空")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if parentID != "" {
		if _, ok := s.locationByIDLocked(parentID); !ok {
			return Location{}, fmt.Errorf("父位置不存在")
		}
	}
	for _, l := range s.db.Locations {
		if l.ParentID == parentID && strings.EqualFold(l.Name, name) {
			return Location{}, fmt.Errorf("同级位置已存在：%s", name)
		}
	}
	loc := Location{ID: newID(), ParentID: parentID, Name: name, ImageFile: imageFile, CreatedAt: now()}
	s.db.Locations = append(s.db.Locations, loc)
	if err := s.save(); err != nil {
		return Location{}, err
	}
	return loc, nil
}

// UpdateLocationImage 替换位置布局图；keepImage 为 true 时保留原图。
func (s *Store) UpdateLocationImage(id, imageFile string, keepImage bool) (Location, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	var old Location
	for i, l := range s.db.Locations {
		if l.ID == id {
			idx = i
			old = l
			break
		}
	}
	if idx < 0 {
		return Location{}, ErrNotFound
	}
	if !keepImage {
		old.ImageFile = imageFile
	}
	s.db.Locations[idx] = old
	if err := s.save(); err != nil {
		return Location{}, err
	}
	return old, nil
}

// DeleteLocation 删除位置；存在物品引用或子位置时拒绝。返回被删记录便于清理图片。
func (s *Store) DeleteLocation(id string) (Location, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, it := range s.db.Items {
		if it.LocationID == id {
			return Location{}, fmt.Errorf("%w: 该位置下仍有物品，无法删除", ErrConflict)
		}
	}
	for _, l := range s.db.Locations {
		if l.ParentID == id {
			return Location{}, fmt.Errorf("%w: 该位置下仍有子位置，无法删除", ErrConflict)
		}
	}
	idx := -1
	var old Location
	for i, l := range s.db.Locations {
		if l.ID == id {
			idx = i
			old = l
			break
		}
	}
	if idx < 0 {
		return Location{}, ErrNotFound
	}
	s.db.Locations = append(s.db.Locations[:idx], s.db.Locations[idx+1:]...)
	if err := s.save(); err != nil {
		return Location{}, err
	}
	return old, nil
}

// locationPathLocked 向上拼接位置完整路径（如：主卧 / 衣柜）。调用方需持有读锁。
func (s *Store) locationPathLocked(id string) string {
	parts := []string{}
	cur, ok := s.locationByIDLocked(id)
	for ok {
		parts = append([]string{cur.Name}, parts...)
		cur, ok = s.locationByIDLocked(cur.ParentID)
	}
	return strings.Join(parts, " / ")
}

// descendantIDs 返回某节点及其全部后代节点 ID。调用方需持有读锁。
func (s *Store) descendantIDs(root string) map[string]bool {
	out := map[string]bool{root: true}
	var walk func(string)
	walk = func(id string) {
		for _, l := range s.db.Locations {
			if l.ParentID == id && !out[l.ID] {
				out[l.ID] = true
				walk(l.ID)
			}
		}
	}
	walk(root)
	return out
}

// pathKey 归一化路径用于比较：去掉所有空白，兼容「主卧/衣柜」与「主卧 / 衣柜」写法。
func pathKey(s string) string {
	return strings.ReplaceAll(strings.TrimSpace(s), " ", "")
}

// resolveLocationLocked 按 ID 解析位置；ID 为空时按完整路径或根级名称查找，都未命中则新建根位置。调用方需持有写锁。
func (s *Store) resolveLocationLocked(id, name string) (Location, error) {
	if id != "" {
		if loc, ok := s.locationByIDLocked(id); ok {
			return loc, nil
		}
		return Location{}, fmt.Errorf("所选位置不存在")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return Location{}, fmt.Errorf("位置不能为空")
	}
	// 先按完整路径精确匹配（兼容子位置）
	key := pathKey(name)
	for _, l := range s.db.Locations {
		if pathKey(s.locationPathLocked(l.ID)) == key {
			return l, nil
		}
	}
	// 再按根级名称匹配
	for _, l := range s.db.Locations {
		if l.ParentID == "" && strings.EqualFold(l.Name, name) {
			return l, nil
		}
	}
	// 都未命中：按 "/" 逐级查找或创建层级（如 主卧 / 衣柜）
	return s.resolveOrCreatePathLocked(name), nil
}

// resolveOrCreatePathLocked 按路径逐级查找位置，缺失的节点自动创建。调用方需持有写锁。
func (s *Store) resolveOrCreatePathLocked(name string) Location {
	parts := []string{}
	for _, p := range strings.Split(name, "/") {
		p = strings.TrimSpace(p)
		if p != "" {
			parts = append(parts, p)
		}
	}
	var parentID string
	var cur Location
	for _, p := range parts {
		found := false
		for _, l := range s.db.Locations {
			if l.ParentID == parentID && strings.EqualFold(l.Name, p) {
				cur = l
				found = true
				break
			}
		}
		if !found {
			cur = Location{ID: newID(), ParentID: parentID, Name: p, CreatedAt: now()}
			s.db.Locations = append(s.db.Locations, cur)
		}
		parentID = cur.ID
	}
	return cur
}

// ---------- 物品 ----------

type ItemFilter struct {
	LocationID   string
	LocationName string
	Tag          string
	Query        string
}

func (s *Store) ListItems(f ItemFilter) []Item {
	s.mu.RLock()
	defer s.mu.RUnlock()
	q := strings.ToLower(strings.TrimSpace(f.Query))
	locName := strings.ToLower(strings.TrimSpace(f.LocationName))
	tag := strings.ToLower(strings.TrimSpace(f.Tag))

	var subtree map[string]bool
	if f.LocationID != "" {
		subtree = s.descendantIDs(f.LocationID)
	}

	// 预计算每个位置的完整路径，便于按父级位置名 / 路径命中子位置物品。
	pathByLoc := make(map[string]string, len(s.db.Locations))
	for _, l := range s.db.Locations {
		pathByLoc[l.ID] = s.locationPathLocked(l.ID)
	}

	out := make([]Item, 0, len(s.db.Items))
	for _, it := range s.db.Items {
		if subtree != nil && !subtree[it.LocationID] {
			continue
		}
		locPath := pathByLoc[it.LocationID]
		if locName != "" {
			if !strings.Contains(strings.ToLower(it.LocationName), locName) &&
				!strings.Contains(strings.ToLower(locPath), locName) {
				continue
			}
		}
		if tag != "" {
			hit := false
			for _, t := range it.Tags {
				if strings.EqualFold(t, f.Tag) {
					hit = true
					break
				}
			}
			if !hit {
				continue
			}
		}
		if q != "" {
			hay := strings.ToLower(it.Name + " " + it.Description + " " + locPath + " " + it.LocationName + " " + it.Barcode + " " + strings.Join(it.Tags, " "))
			if !strings.Contains(hay, q) {
				continue
			}
		}
		out = append(out, it)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt > out[j].UpdatedAt })
	return out
}

// AllTags 返回全部物品去重后的标签，按名称排序。
func (s *Store) AllTags() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := map[string]string{}
	for _, it := range s.db.Items {
		for _, t := range it.Tags {
			key := strings.ToLower(t)
			if _, ok := seen[key]; !ok {
				seen[key] = t
			}
		}
	}
	out := make([]string, 0, len(seen))
	for _, v := range seen {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// RenameTag 把所有物品上的旧标签改名为新标签；新标签与既有标签同名时自动合并去重。
// 未找到旧标签时返回 ErrNotFound。
func (s *Store) RenameTag(oldName, newName string) error {
	oldName = strings.TrimSpace(oldName)
	newName = strings.TrimSpace(newName)
	if oldName == "" {
		return fmt.Errorf("原标签不能为空")
	}
	if newName == "" {
		return fmt.Errorf("新标签不能为空")
	}
	if strings.EqualFold(oldName, newName) {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for i := range s.db.Items {
		tags := s.db.Items[i].Tags
		replaced := false
		seen := map[string]bool{}
		out := make([]string, 0, len(tags))
		for _, t := range tags {
			if strings.EqualFold(t, oldName) {
				replaced = true
				t = newName
			}
			key := strings.ToLower(t)
			if !seen[key] {
				seen[key] = true
				out = append(out, t)
			}
		}
		if replaced {
			s.db.Items[i].Tags = out
			changed = true
		}
	}
	if !changed {
		return ErrNotFound
	}
	return s.save()
}

// SummarySegment 是汇总饼图的一个分片：Count 为物品件数，Quantity 为数量合计。
type SummarySegment struct {
	Name     string `json:"name"`
	Count    int    `json:"count"`
	Quantity int    `json:"quantity"`
}

// TopItem 是数量排行中的一件物品。
type TopItem struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Quantity int    `json:"quantity"`
	Location string `json:"location"`
}

// SummaryResult 是仪表盘汇总数据：位置 / 标签分布 + 数量 Top 物品。
type SummaryResult struct {
	ByLocation []SummarySegment `json:"byLocation"`
	ByTag      []SummarySegment `json:"byTag"`
	TopItems   []TopItem        `json:"topItems"`
}

// Summary 按位置、按标签汇总全部物品，并给出数量最高的前 10 件物品；
// 无位置归入「未分配位置」，无标签归入「无标签」。
func (s *Store) Summary() SummaryResult {
	s.mu.RLock()
	defer s.mu.RUnlock()

	locMap := map[string]*SummarySegment{}
	tagMap := map[string]*SummarySegment{}
	add := func(m map[string]*SummarySegment, name string, qty int) {
		seg, ok := m[name]
		if !ok {
			seg = &SummarySegment{Name: name}
			m[name] = seg
		}
		seg.Count++
		seg.Quantity += qty
	}

	var topPool []TopItem
	for _, it := range s.db.Items {
		locName := s.locationPathLocked(it.LocationID)
		if locName == "" {
			locName = "未分配位置"
		}
		add(locMap, locName, it.Quantity)
		if len(it.Tags) == 0 {
			add(tagMap, "无标签", it.Quantity)
		} else {
			for _, t := range it.Tags {
				add(tagMap, t, it.Quantity)
			}
		}
		topPool = append(topPool, TopItem{
			ID:       it.ID,
			Name:     it.Name,
			Quantity: it.Quantity,
			Location: locName,
		})
	}

	toSlice := func(m map[string]*SummarySegment) []SummarySegment {
		out := make([]SummarySegment, 0, len(m))
		for _, seg := range m {
			out = append(out, *seg)
		}
		sort.Slice(out, func(i, j int) bool {
			if out[i].Count != out[j].Count {
				return out[i].Count > out[j].Count
			}
			return out[i].Name < out[j].Name
		})
		return out
	}

	sort.Slice(topPool, func(i, j int) bool {
		if topPool[i].Quantity != topPool[j].Quantity {
			return topPool[i].Quantity > topPool[j].Quantity
		}
		return topPool[i].Name < topPool[j].Name
	})
	if len(topPool) > 10 {
		topPool = topPool[:10]
	}

	return SummaryResult{
		ByLocation: toSlice(locMap),
		ByTag:      toSlice(tagMap),
		TopItems:   topPool,
	}
}

func (s *Store) GetItem(id string) (Item, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, it := range s.db.Items {
		if it.ID == id {
			return it, nil
		}
	}
	return Item{}, ErrNotFound
}

func (s *Store) AddItem(in Item, imageFile string) (Item, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return Item{}, fmt.Errorf("物品名称不能为空")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	loc, err := s.resolveLocationLocked(in.LocationID, in.LocationName)
	if err != nil {
		return Item{}, err
	}
	if in.Quantity <= 0 {
		in.Quantity = 1
	}
	ts := now()
	in.ID = newID()
	in.LocationID = loc.ID
	in.LocationName = loc.Name
	in.Barcode = strings.TrimSpace(in.Barcode)
	in.Tags = normalizeTags(strings.Join(in.Tags, ","))
	in.ImageFile = imageFile
	in.CreatedAt = ts
	in.UpdatedAt = ts
	s.db.Items = append(s.db.Items, in)
	if err := s.save(); err != nil {
		return Item{}, err
	}
	return in, nil
}

func (s *Store) UpdateItem(id string, in Item, imageFile string, keepImage bool) (Item, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return Item{}, fmt.Errorf("物品名称不能为空")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	var old Item
	for i, it := range s.db.Items {
		if it.ID == id {
			idx = i
			old = it
			break
		}
	}
	if idx < 0 {
		return Item{}, ErrNotFound
	}
	loc, err := s.resolveLocationLocked(in.LocationID, in.LocationName)
	if err != nil {
		return Item{}, err
	}
	if in.Quantity <= 0 {
		in.Quantity = 1
	}
	old.Name = in.Name
	old.Description = in.Description
	old.LocationID = loc.ID
	old.LocationName = loc.Name
	old.Quantity = in.Quantity
	old.Barcode = strings.TrimSpace(in.Barcode)
	old.Tags = normalizeTags(strings.Join(in.Tags, ","))
	old.UpdatedAt = now()
	if !keepImage {
		old.ImageFile = imageFile
	}
	s.db.Items[idx] = old
	if err := s.save(); err != nil {
		return Item{}, err
	}
	return old, nil
}

func (s *Store) DeleteItem(id string) (Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	var old Item
	for i, it := range s.db.Items {
		if it.ID == id {
			idx = i
			old = it
			break
		}
	}
	if idx < 0 {
		return Item{}, ErrNotFound
	}
	s.db.Items = append(s.db.Items[:idx], s.db.Items[idx+1:]...)
	if err := s.save(); err != nil {
		return Item{}, err
	}
	return old, nil
}

// ---------- 账号 ----------

// UserCount 返回已注册账号数量。
func (s *Store) UserCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.db.Users)
}

// RegisterUser 注册首个账号；系统已有账号时拒绝（ErrConflict）。
func (s *Store) RegisterUser(username, password string) (User, error) {
	username = strings.TrimSpace(username)
	if r := []rune(username); len(r) < 2 || len(r) > 30 {
		return User{}, fmt.Errorf("用户名长度需为 2–30 个字符")
	}
	if len(password) < 6 || len(password) > 64 {
		return User{}, fmt.Errorf("密码长度需为 6–64 个字符")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.db.Users) > 0 {
		return User{}, fmt.Errorf("%w: 系统已存在账号，请直接登录", ErrConflict)
	}
	for _, u := range s.db.Users {
		if strings.EqualFold(u.Username, username) {
			return User{}, fmt.Errorf("%w: 用户名已存在", ErrConflict)
		}
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return User{}, err
	}
	u := User{ID: newID(), Username: username, PasswordHash: string(hash), CreatedAt: now()}
	s.db.Users = append(s.db.Users, u)
	if err := s.save(); err != nil {
		return User{}, err
	}
	return u, nil
}

// VerifyUser 校验用户名密码；失败返回通用错误，避免枚举账号。
func (s *Store) VerifyUser(username, password string) (User, error) {
	username = strings.TrimSpace(username)
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, u := range s.db.Users {
		if strings.EqualFold(u.Username, username) {
			if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) == nil {
				return u, nil
			}
			return User{}, fmt.Errorf("用户名或密码错误")
		}
	}
	return User{}, fmt.Errorf("用户名或密码错误")
}

// Stats 返回主页仪表盘统计数据。
func (s *Store) Stats() map[string]int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	withImage := 0
	for _, it := range s.db.Items {
		if it.ImageFile != "" {
			withImage++
		}
	}
	return map[string]int{
		"items":      len(s.db.Items),
		"locations":  len(s.db.Locations),
		"withImage":  withImage,
	}
}
