package main

import (
	"bufio"
	"crypto/rand"
	"embed"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	qrcode "github.com/skip2/go-qrcode"
)

//go:embed web
var webFS embed.FS

const maxUploadBytes = 10 << 20 // 10 MB

var allowedImageExt = map[string]string{
	"image/jpeg": ".jpg",
	"image/jpg":  ".jpg",
	"image/png":  ".png",
	"image/webp": ".webp",
	"image/gif":  ".gif",
}

type server struct {
	store    *Store
	sessions sync.Map // token -> username
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func decodeJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	return dec.Decode(v)
}

func (s *server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "time": now()})
}

// ---------- 账号 / 会话 ----------

func (s *server) createSession(username string) string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	token := hex.EncodeToString(b)
	s.sessions.Store(token, username)
	return token
}

// sessionUser 从 Authorization: Bearer <token> 解析当前登录用户。
func (s *server) sessionUser(r *http.Request) (string, bool) {
	auth := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(auth, prefix) {
		return "", false
	}
	token := strings.TrimSpace(auth[len(prefix):])
	if token == "" {
		return "", false
	}
	v, ok := s.sessions.Load(token)
	if !ok {
		return "", false
	}
	name, _ := v.(string)
	return name, true
}

// authStatus 返回是否已有账号、当前是否已登录。无需鉴权。
func (s *server) authStatus(w http.ResponseWriter, r *http.Request) {
	_, ok := s.sessionUser(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"registered":    s.store.UserCount() > 0,
		"authenticated": ok,
	})
}

// authRegister 首次使用注册账号；系统已有账号时拒绝。
func (s *server) authRegister(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Confirm  string `json:"confirm"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体不是合法 JSON"})
		return
	}
	if body.Password != body.Confirm {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "两次输入的密码不一致"})
		return
	}
	u, err := s.store.RegisterUser(body.Username, body.Password)
	if err != nil {
		if errors.Is(err, ErrConflict) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	token := s.createSession(u.Username)
	writeJSON(w, http.StatusCreated, map[string]any{"token": token, "username": u.Username})
}

// authLogin 校验账号密码并签发会话 token。
func (s *server) authLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体不是合法 JSON"})
		return
	}
	u, err := s.store.VerifyUser(body.Username, body.Password)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
		return
	}
	token := s.createSession(u.Username)
	writeJSON(w, http.StatusOK, map[string]any{"token": token, "username": u.Username})
}

// authLogout 注销当前会话；token 缺失也返回成功。
func (s *server) authLogout(w http.ResponseWriter, r *http.Request) {
	auth := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if strings.HasPrefix(auth, prefix) {
		s.sessions.Delete(strings.TrimSpace(auth[len(prefix):]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// isPublic 判断无需登录即可访问的路径：健康检查、鉴权接口、静态页面与上传图片。
func (s *server) isPublic(r *http.Request) bool {
	p := r.URL.Path
	if p == "/api/health" {
		return true
	}
	if strings.HasPrefix(p, "/api/auth/") {
		return true
	}
	// 二维码 PNG 由 <img> 标签直接加载，无法携带请求头，保持公开（仅把文本编码为二维码）。
	if p == "/api/qr" {
		return true
	}
	if !strings.HasPrefix(p, "/api/") {
		return true
	}
	return false
}

// authMiddleware 保护需要登录的 API；未登录返回 401。
func (s *server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.isPublic(r) {
			next.ServeHTTP(w, r)
			return
		}
		if _, ok := s.sessionUser(r); ok {
			next.ServeHTTP(w, r)
			return
		}
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "未登录或登录已过期"})
	})
}

func (s *server) stats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.store.Stats())
}

func (s *server) summary(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.store.Summary())
}

// renameTag 重命名标签：同步更新到所有使用该标签的物品。
func (s *server) renameTag(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Old string `json:"old"`
		New string `json:"new"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体不是合法 JSON"})
		return
	}
	err := s.store.RenameTag(body.Old, body.New)
	switch {
	case errors.Is(err, ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "标签不存在"})
	case err != nil:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	default:
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "tags": s.store.AllTags()})
	}
}

// ---------- 位置 ----------

func (s *server) listLocations(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.store.ListLocations())
}

func (s *server) createLocation(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name     string `json:"name"`
		ParentID string `json:"parentId"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求体不是合法 JSON"})
		return
	}
	loc, err := s.store.AddLocation(body.Name, body.ParentID, "")
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, loc)
}

// uploadLocationImage 上传或替换某位置的布局图片。
func (s *server) uploadLocationImage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	prev, ok := s.store.LocationByID(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "位置不存在"})
		return
	}
	newImage, _, err := s.saveImage(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	keepImage := newImage == ""
	updated, err := s.store.UpdateLocationImage(id, newImage, keepImage)
	if err != nil {
		s.removeImage(newImage)
		if errors.Is(err, ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "位置不存在"})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if !keepImage && prev.ImageFile != "" && prev.ImageFile != newImage {
		s.removeImage(prev.ImageFile)
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *server) deleteLocation(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	old, err := s.store.DeleteLocation(id)
	switch {
	case errors.Is(err, ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "位置不存在"})
	case errors.Is(err, ErrConflict):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
	case err != nil:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
	default:
		s.removeImage(old.ImageFile)
		writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
	}
}

// ---------- 物品 ----------

func (s *server) listItems(w http.ResponseWriter, r *http.Request) {
	locParam := r.URL.Query().Get("location")
	f := ItemFilter{Tag: r.URL.Query().Get("tag"), Query: r.URL.Query().Get("q")}
	if locParam != "" {
		if _, ok := s.store.LocationByID(locParam); ok {
			f.LocationID = locParam
		} else {
			f.LocationName = locParam
		}
	}
	writeJSON(w, http.StatusOK, s.store.ListItems(f))
}

func (s *server) listTags(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.store.AllTags())
}

func (s *server) getItem(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	it, err := s.store.GetItem(id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "物品不存在"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, it)
}

// qrCode 根据文本生成二维码 PNG。
func (s *server) qrCode(w http.ResponseWriter, r *http.Request) {
	text := strings.TrimSpace(r.URL.Query().Get("text"))
	if text == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "缺少二维码内容"})
		return
	}
	if len(text) > 500 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "二维码内容过长"})
		return
	}
	png, err := qrcode.Encode(text, qrcode.Medium, 256)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "二维码生成失败"})
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(png)
}

func (s *server) saveImage(r *http.Request) (string, string, error) {
	file, header, err := r.FormFile("image")
	if err != nil {
		// 没有上传图片是允许的
		return "", "", nil
	}
	defer file.Close()

	ctype := header.Header.Get("Content-Type")
	ext, ok := allowedImageExt[ctype]
	if !ok {
		// 退回到文件名后缀判断
		ext = strings.ToLower(filepath.Ext(header.Filename))
		switch ext {
		case ".jpg", ".jpeg", ".png", ".webp", ".gif":
			if ext == ".jpeg" {
				ext = ".jpg"
			}
		default:
			return "", "", fmt.Errorf("不支持的图片类型：%s", ctype)
		}
	}

	b := make([]byte, 16)
	_, _ = rand.Read(b)
	name := hex.EncodeToString(b) + ext
	dst := filepath.Join(s.store.UploadDir(), name)
	out, err := os.Create(dst)
	if err != nil {
		return "", "", err
	}
	defer out.Close()
	if _, err := copyLimited(out, file, maxUploadBytes+1); err != nil {
		_ = os.Remove(dst)
		return "", "", err
	}
	return name, header.Filename, nil
}

func copyLimited(dst *os.File, src io.Reader, limit int64) (int64, error) {
	buf := make([]byte, 32*1024)
	var total int64
	for {
		n, err := src.Read(buf)
		if n > 0 {
			total += int64(n)
			if total > limit {
				return total, fmt.Errorf("图片大小超过 10MB 限制")
			}
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return total, werr
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return total, nil
			}
			return total, err
		}
	}
}

func (s *server) removeImage(name string) {
	if name == "" {
		return
	}
	clean := filepath.Base(name)
	_ = os.Remove(filepath.Join(s.store.UploadDir(), clean))
}

func parseItemForm(r *http.Request) (Item, error) {
	if err := r.ParseMultipartForm(maxUploadBytes); err != nil {
		return Item{}, err
	}
	qty, _ := strconv.Atoi(r.FormValue("quantity"))
	return Item{
		Name:         r.FormValue("name"),
		Description:  r.FormValue("description"),
		LocationID:  r.FormValue("locationId"),
		LocationName: r.FormValue("locationName"),
		Barcode:     r.FormValue("barcode"),
		Tags:        normalizeTags(r.FormValue("tags")),
		Quantity:    qty,
	}, nil
}

func (s *server) createItem(w http.ResponseWriter, r *http.Request) {
	in, err := parseItemForm(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	imageFile, _, err := s.saveImage(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	item, err := s.store.AddItem(in, imageFile)
	if err != nil {
		s.removeImage(imageFile)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

// csvColumnAliases 把中英文表头列名映射到标准字段。
var csvColumnAliases = map[string]string{
	"名称": "name", "物品名称": "name", "name": "name", "title": "name",
	"位置": "location", "所在位置": "location", "位置路径": "location", "location": "location",
	"标签": "tags", "tag": "tags", "tags": "tags",
	"条码": "barcode", "编号": "barcode", "条码/编号": "barcode", "barcode": "barcode",
	"数量": "quantity", "quantity": "quantity", "qty": "quantity",
	"备注": "description", "描述": "description", "备注描述": "description", "description": "description",
}

// importItems 解析 CSV 批量导入物品；返回成功条数、失败条数与逐行错误明细。
func (s *server) importItems(w http.ResponseWriter, r *http.Request) {
	file, _, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "未选择上传的 CSV 文件"})
		return
	}
	defer file.Close()

	reader := csv.NewReader(bufio.NewReader(file))
	reader.LazyQuotes = true
	reader.TrimLeadingSpace = true
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "CSV 解析失败：" + err.Error()})
		return
	}
	if len(records) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "文件内容为空"})
		return
	}

	// 解析表头；识别不到「名称」列时按固定顺序：名称,位置,标签,条码,数量,备注
	colIdx := map[string]int{}
	for i, h := range records[0] {
		clean := strings.TrimPrefix(strings.TrimSpace(h), "\ufeff")
		key := csvColumnAliases[clean]
		if key == "" {
			key = csvColumnAliases[strings.ToLower(clean)]
		}
		if key != "" {
			colIdx[key] = i
		}
	}
	startRow := 1
	if _, ok := colIdx["name"]; !ok {
		colIdx = map[string]int{"name": 0, "location": 1, "tags": 2, "barcode": 3, "quantity": 4, "description": 5}
		startRow = 0
	}

	getField := func(row []string, key string) string {
		i, ok := colIdx[key]
		if !ok || i >= len(row) {
			return ""
		}
		return strings.TrimSpace(row[i])
	}

	added, failed := 0, 0
	errs := []map[string]any{}
	for ri, row := range records[startRow:] {
		rowNo := startRow + ri + 1
		name := getField(row, "name")
		if name == "" {
			continue // 跳过空行
		}
		qty, _ := strconv.Atoi(getField(row, "quantity"))
		in := Item{
			Name:         name,
			LocationName: getField(row, "location"),
			Barcode:      getField(row, "barcode"),
			Description:  getField(row, "description"),
			Quantity:     qty,
			Tags:         normalizeTags(getField(row, "tags")),
		}
		if _, err := s.store.AddItem(in, ""); err != nil {
			failed++
			errs = append(errs, map[string]any{"row": rowNo, "name": name, "reason": err.Error()})
			continue
		}
		added++
	}

	writeJSON(w, http.StatusOK, map[string]any{"added": added, "failed": failed, "errors": errs})
}

func (s *server) updateItem(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	in, err := parseItemForm(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	// 记录更新前的图片，便于替换后清理
	prev, _ := s.store.GetItem(id)
	newImage, _, err := s.saveImage(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	keepImage := newImage == ""
	updated, err := s.store.UpdateItem(id, in, newImage, keepImage)
	if err != nil {
		s.removeImage(newImage)
		if errors.Is(err, ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "物品不存在"})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	// 替换图片后清理旧图
	if !keepImage && prev.ImageFile != "" && prev.ImageFile != newImage {
		s.removeImage(prev.ImageFile)
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *server) deleteItem(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	old, err := s.store.DeleteItem(id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "物品不存在"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.removeImage(old.ImageFile)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
}

func (s *server) serveUpload(w http.ResponseWriter, r *http.Request) {
	name := filepath.Base(r.PathValue("file"))
	if name == "" || name == "." {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, filepath.Join(s.store.UploadDir(), name))
}

func main() {
	dataDir := os.Getenv("INV_DATA")
	if dataDir == "" {
		dataDir = "/data"
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	store, err := NewStore(dataDir)
	if err != nil {
		log.Fatalf("初始化存储失败：%v", err)
	}
	s := &server{store: store}

	sub, _ := fs.Sub(webFS, "web")
	fileServer := http.FileServer(http.FS(sub))

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/auth/status", s.authStatus)
	mux.HandleFunc("POST /api/auth/register", s.authRegister)
	mux.HandleFunc("POST /api/auth/login", s.authLogin)
	mux.HandleFunc("POST /api/auth/logout", s.authLogout)
	mux.HandleFunc("GET /api/stats", s.stats)
	mux.HandleFunc("GET /api/summary", s.summary)
	mux.HandleFunc("PUT /api/tags/rename", s.renameTag)
	mux.HandleFunc("GET /api/locations", s.listLocations)
	mux.HandleFunc("POST /api/locations", s.createLocation)
	mux.HandleFunc("POST /api/locations/{id}/image", s.uploadLocationImage)
	mux.HandleFunc("DELETE /api/locations/{id}", s.deleteLocation)
	mux.HandleFunc("GET /api/tags", s.listTags)
	mux.HandleFunc("GET /api/items", s.listItems)
	mux.HandleFunc("GET /api/items/{id}", s.getItem)
	mux.HandleFunc("POST /api/items", s.createItem)
	mux.HandleFunc("POST /api/items/import", s.importItems)
	mux.HandleFunc("PUT /api/items/{id}", s.updateItem)
	mux.HandleFunc("DELETE /api/items/{id}", s.deleteItem)
	mux.HandleFunc("GET /api/qr", s.qrCode)
	// 图片文件由 <img> 标签直接引用，无法携带请求头，保持开放（文件名为随机十六进制）。
	mux.HandleFunc("GET /uploads/{file}", s.serveUpload)
	mux.Handle("GET /", fileServer)

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      logging(recoverer(s.authMiddleware(mux))),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}

	log.Printf("家庭物资管理服务已启动，监听 :%s，数据目录 %s", port, dataDir)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s (%s)", r.Method, r.URL.Path, time.Since(start))
	})
}

func recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("panic: %v", rec)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "服务器内部错误"})
			}
		}()
		next.ServeHTTP(w, r)
	})
}
