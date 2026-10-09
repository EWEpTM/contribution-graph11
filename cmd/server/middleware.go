package main

import (
	"net/http"
	"net/url"
	"os"
	"strings"
)

type ctxKey string

const userKey ctxKey = "auth_user"

func corsMiddleware(next http.Handler) http.Handler {
	allowedOrigin := os.Getenv("ALLOWED_ORIGIN")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {

		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data:; connect-src 'self'; font-src 'self'; object-src 'none'; base-uri 'self'; form-action 'self'; frame-ancestors 'none'")

		if allowedOrigin != "" {
			if origin := r.Header.Get("Origin"); origin == allowedOrigin {
				w.Header().Set("Access-Control-Allow-Origin", allowedOrigin)
				w.Header().Set("Access-Control-Allow-Credentials", "true")
				w.Header().Set("Vary", "Origin")
			}
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		}

		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			if origin := r.Header.Get("Origin"); origin != "" {
				if !(allowedOrigin != "" && origin == allowedOrigin) && !isSameOriginHost(r) {
					writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-origin request denied"})
					return
				}
			}
		}

		p := r.URL.Path
		if strings.HasPrefix(p, "/api/") {
			w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		} else if p == "/" || strings.HasSuffix(p, ".html") || p == "/manifest.json" || p == "/sw.js" {
			w.Header().Set("Cache-Control", "no-cache")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=604800")
		}

		if r.Method == http.MethodOptions {
			if allowedOrigin == "" {

				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isSameOriginHost 校验请求 Origin 的 host 与请求 Host 一致（同源浏览器请求）
func isSameOriginHost(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

// PWA 必备资源必须免登录公开，否则浏览器拿不到 manifest、
// 注册不了 Service Worker、校验不了图标，导致无法安装为 PWA。
// 注意：/theme.js、/common.js、/index.css、/index.js 与 /vendor/echarts.min.js
// 被登录页引用或位于 sw.js 预缓存列表，必须一并公开，否则未登录时被 303 重定向、预缓存失败。
func isPublicPwaAsset(path string) bool {
	switch path {
	case "/manifest.json",
		"/sw.js",
		"/theme.js",
		"/common.js",
		"/index.css",
		"/index.js",
		"/vendor/echarts.min.js",
		"/icon-192.png",
		"/icon-512.png",
		"/apple-touch-icon.png",
		"/favicon-16x16.png",
		"/favicon-32x32.png":
		return true
	}
	return false
}
