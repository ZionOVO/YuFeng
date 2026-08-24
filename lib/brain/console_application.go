package brain

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// NewConsoleHandler 把控制台静态产物托管在 /app。
// 未知 /app/* 回退 index.html，供单页路由与 curl 验收。
//
// [人机交付闭环]: ../../docs/glossary.md#human-delivery
func NewConsoleHandler(dir string) http.Handler {
	mux := http.NewServeMux()
	MountConsole(mux, dir)
	return mux
}

// MountConsole 把 console/dist 挂到 mux 的 /app；目录不存在则不挂。
func MountConsole(mux *http.ServeMux, dir string) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return
	}
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		return
	}
	mux.Handle("/app/", consoleSPA(dir))
	mux.HandleFunc("/app", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/app/", http.StatusFound)
	})
}

func consoleSPA(dir string) http.Handler {
	index := filepath.Join(dir, "index.html")
	root := filepath.Clean(dir)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rel := strings.TrimPrefix(r.URL.Path, "/app/")
		rel = strings.TrimPrefix(filepath.Clean("/"+rel), "/")
		target := root
		if rel != "." && rel != "" {
			target = filepath.Join(root, rel)
		}
		if !insideDir(root, target) {
			http.NotFound(w, r)
			return
		}
		info, err := os.Stat(target)
		if err == nil && !info.IsDir() {
			http.ServeFile(w, r, target)
			return
		}
		http.ServeFile(w, r, index)
	})
}

func insideDir(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}
