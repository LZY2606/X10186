package web

// server.go: 页脉探针 HTTP 服务。

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"pagepulse/internal/pdf"
	"pagepulse/internal/store"
)

//go:embed webui/index.html
var uiFS embed.FS

// Server HTTP 处理器集合。
type Server struct {
	st *store.Store
}

// New 构造 Server。
func New(st *store.Store) *Server { return &Server{st: st} }

// Mux 注册路由。
func (s *Server) Mux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.index)
	mux.HandleFunc("/api/documents", s.documents)
	mux.HandleFunc("/api/documents/", s.documentSub)
	mux.HandleFunc("/api/import", s.importDoc)
	return mux
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	b, err := uiFS.ReadFile("webui/index.html")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(b)
}

func writeJSON(w http.ResponseWriter, v any, code int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, map[string]string{"error": msg}, code)
}

func (s *Server) documents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, 405, "method not allowed")
		return
	}
	docs, err := s.st.ListDocuments()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, docs, 200)
}

func (s *Server) importDoc(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method not allowed")
		return
	}
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		writeErr(w, 400, "无法解析上传表单: "+err.Error())
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeErr(w, 400, "缺少 file 字段")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		writeErr(w, 400, "读取上传失败")
		return
	}
	report := pdf.ScanFile(data, header.Filename, pdf.DefaultLimits())
	doc, err := s.st.Import(header.Filename, data, report)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]any{"id": doc.ID, "report": report}, 200)
}

// 路径形如 /api/documents/{id}/report|bytes|branches|...
func (s *Server) documentSub(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/documents/"), "/")
	if len(parts) < 2 || parts[0] == "" {
		writeErr(w, 404, "not found")
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		writeErr(w, 400, "bad doc id")
		return
	}
	action := parts[1]
	switch action {
	case "report":
		s.getReport(w, r, id)
	case "bytes":
		s.getBytes(w, r, id)
	case "branches":
		s.branches(w, r, id, parts[2:])
	case "graph":
		s.graph(w, r, id)
	case "export":
		s.export(w, r, id)
	default:
		writeErr(w, 404, "unknown action "+action)
	}
}

func (s *Server) getReport(w http.ResponseWriter, r *http.Request, id int64) {
	doc, err := s.st.GetDocument(id)
	if err != nil {
		writeErr(w, 404, err.Error())
		return
	}
	writeJSON(w, doc.Report, 200)
}

func (s *Server) getBytes(w http.ResponseWriter, r *http.Request, id int64) {
	doc, err := s.st.GetDocument(id)
	if err != nil {
		writeErr(w, 404, err.Error())
		return
	}
	off, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	length, _ := strconv.Atoi(r.URL.Query().Get("length"))
	if length <= 0 || length > 4096 {
		length = 256
	}
	data, err := s.st.ReadOriginal(doc)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if off < 0 || off > len(data) {
		writeErr(w, 400, "offset out of range")
		return
	}
	end := off + length
	if end > len(data) {
		end = len(data)
	}
	writeJSON(w, map[string]any{
		"offset": off, "length": end - off,
		"hex": hexDump(data[off:end], off),
	}, 200)
}

func (s *Server) branches(w http.ResponseWriter, r *http.Request, id int64, rest []string) {
	if r.Method == http.MethodPost && len(rest) >= 1 && rest[0] == "create" {
		var req struct {
			Name   string `json:"name"`
			Parent int64  `json:"parentId"`
			Note   string `json:"note"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Name) == "" {
			writeErr(w, 400, "需要分支名 name")
			return
		}
		var parent *int64
		if req.Parent != 0 {
			p := req.Parent
			parent = &p
		}
		b, err := s.st.CreateBranch(id, strings.TrimSpace(req.Name), parent, req.Note)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		writeJSON(w, b, 200)
		return
	}
	if r.Method == http.MethodPost && len(rest) >= 2 && rest[0] != "" && rest[1] == "choose" {
		branchID, err := strconv.ParseInt(rest[0], 10, 64)
		if err != nil {
			writeErr(w, 400, "bad branch id")
			return
		}
		var req struct {
			ObjNum   int    `json:"objNum"`
			Revision int    `json:"revision"`
			CandID   int    `json:"candId"`
			Note     string `json:"note"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, 400, "bad body")
			return
		}
		ch, err := s.st.ChooseCandidate(id, branchID, req.ObjNum, req.Revision, req.CandID, req.Note)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		writeJSON(w, ch, 200)
		return
	}
	// 默认列表
	list, err := s.st.ListBranches(id)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, list, 200)
}

func (s *Server) graph(w http.ResponseWriter, r *http.Request, id int64) {
	branchID, _ := strconv.ParseInt(r.URL.Query().Get("branch"), 10, 64)
	if branchID == 0 {
		bid, err := s.st.DefaultBranchID(id)
		if err != nil {
			writeErr(w, 404, err.Error())
			return
		}
		branchID = bid
	}
	g, err := s.st.Resolve(id, branchID)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, g, 200)
}

func (s *Server) export(w http.ResponseWriter, r *http.Request, id int64) {
	branchID, _ := strconv.ParseInt(r.URL.Query().Get("branch"), 10, 64)
	if branchID == 0 {
		bid, err := s.st.DefaultBranchID(id)
		if err != nil {
			writeErr(w, 404, err.Error())
			return
		}
		branchID = bid
	}
	bundle, err := s.st.BuildExport(id, branchID)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=review-doc%d.zip", id))
	w.Write(bundle.Zip)
}

// hexDump 生成带偏移、十六进制、ASCII 三列的片段。
func hexDump(data []byte, base int) []map[string]any {
	var rows []map[string]any
	for i := 0; i < len(data); i += 16 {
		end := i + 16
		if end > len(data) {
			end = len(data)
		}
		chunk := data[i:end]
		hexs := make([]string, len(chunk))
		ascii := make([]byte, len(chunk))
		for j, b := range chunk {
			hexs[j] = fmt.Sprintf("%02X", b)
			if b >= 32 && b < 127 {
				ascii[j] = b
			} else {
				ascii[j] = '.'
			}
		}
		rows = append(rows, map[string]any{
			"offset": base + i,
			"hex":    strings.Join(hexs, " "),
			"ascii":  string(ascii),
		})
	}
	return rows
}
