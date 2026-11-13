package httpapi

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	mem "github.com/musi-ai/education/internal/modules/student/memory"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type Accounts struct{ DB *sql.DB }

func (a *Accounts) Migrate() error {
	for _, q := range []string{`CREATE TABLE IF NOT EXISTS student_account(username VARCHAR(80) PRIMARY KEY,student_id VARCHAR(128) NOT NULL UNIQUE,salt VARCHAR(64) NOT NULL,password_hash VARCHAR(128) NOT NULL)`, `CREATE TABLE IF NOT EXISTS student_session(token_hash VARCHAR(64) PRIMARY KEY,student_id VARCHAR(128) NOT NULL,expires_at DATETIME NOT NULL)`} {
		if _, e := a.DB.Exec(q); e != nil {
			return e
		}
	}
	return nil
}
func accountToken() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func tokenHash(v string) string { h := sha256.Sum256([]byte(v)); return hex.EncodeToString(h[:]) }
func passwordHash(p, salt string) string {
	v, _ := pbkdf2.Key(sha256.New, p, []byte(salt), 210000, 32)
	return hex.EncodeToString(v)
}
func (s Server) accountHandler(w http.ResponseWriter, r *http.Request) {
	if origin := r.Header.Get("Origin"); origin != "" {
		u, e := url.Parse(origin)
		if e != nil || u.Host != r.Host {
			failure(w, 403, "不允许跨站账号请求")
			return
		}
	}
	a := s.Accounts
	action := strings.TrimPrefix(r.URL.Path, "/api/v1/account/")
	if action == "current" && r.Method == "GET" {
		id := s.StudentID
		logged := false
		if cookie, e := r.Cookie("musi_session"); e == nil {
			if e = a.DB.QueryRowContext(r.Context(), "SELECT student_id FROM student_session WHERE token_hash=? AND expires_at>UTC_TIMESTAMP()", tokenHash(cookie.Value)).Scan(&id); e == nil {
				logged = true
			}
		}
		writeJSON(w, 200, map[string]any{"student_id": id, "logged_in": logged, "temporary": !logged})
		return
	}
	if r.Method != "POST" {
		failure(w, 405, "请求方式不正确")
		return
	}
	if action == "logout" {
		if c, e := r.Cookie("musi_session"); e == nil {
			a.DB.ExecContext(r.Context(), "DELETE FROM student_session WHERE token_hash=?", tokenHash(c.Value))
		}
		http.SetCookie(w, &http.Cookie{Name: "musi_session", Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1})
		writeJSON(w, 200, map[string]bool{"ok": true})
		return
	}
	var body struct {
		Username string           `json:"username"`
		Password string           `json:"password"`
		Basic    mem.BasicProfile `json:"basic"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&body) != nil {
		failure(w, 400, "填写信息格式不正确")
		return
	}
	body.Username = strings.ToLower(strings.TrimSpace(body.Username))
	if !regexp.MustCompile(`^[a-z0-9_-]{3,40}$`).MatchString(body.Username) || len(body.Password) < 8 || len(body.Password) > 128 {
		failure(w, 400, "账号需3–40位字母数字或下划线，密码需8–128位")
		return
	}
	var id, salt, hash string
	switch action {
	case "register":
		if !validBasic(body.Basic) || strings.TrimSpace(body.Basic.Name) == "" || strings.TrimSpace(body.Basic.Grade) == "" {
			failure(w, 400, "请完善姓名和年级")
			return
		}
		id = "student-" + accountToken()[:24]
		salt = accountToken()
		hash = passwordHash(body.Password, salt)
		tx, e := a.DB.BeginTx(r.Context(), nil)
		if e != nil {
			failure(w, 503, "注册暂不可用")
			return
		}
		defer tx.Rollback()
		if _, e = tx.ExecContext(r.Context(), "INSERT INTO student_account VALUES(?,?,?,?)", body.Username, id, salt, hash); e != nil {
			failure(w, 409, "账号已存在或暂不可注册")
			return
		}
		if s.StudentMemory == nil {
			failure(w, 503, "学生档案尚未配置")
			return
		}
		if _, e = s.StudentMemory.Repo.SaveBasic(r.Context(), id, body.Basic, 0); e != nil {
			failure(w, 503, "保存基本信息失败")
			return
		}
		if e = tx.Commit(); e != nil {
			failure(w, 503, "注册未完成")
			return
		}
	case "login":
		e := a.DB.QueryRowContext(r.Context(), "SELECT student_id,salt,password_hash FROM student_account WHERE username=?", body.Username).Scan(&id, &salt, &hash)
		if e != nil {
			salt = "dummy"
			hash = "invalid"
		}
		got := passwordHash(body.Password, salt)
		if e != nil || subtle.ConstantTimeCompare([]byte(got), []byte(hash)) != 1 {
			failure(w, 401, "账号或密码不正确")
			return
		}
	default:
		failure(w, 404, "账号接口不存在")
		return
	}
	token := accountToken()
	if _, e := a.DB.ExecContext(r.Context(), "INSERT INTO student_session VALUES(?,?,?)", tokenHash(token), id, time.Now().UTC().Add(30*24*time.Hour)); e != nil {
		failure(w, 503, "登录状态保存失败")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "musi_session", Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 30 * 86400})
	writeJSON(w, 200, map[string]any{"student_id": id, "ok": true})
}
func (s Server) accountMiddleware() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/account/") {
			s.accountHandler(w, r)
			return
		}
		copy := s
		copy.Accounts = nil
		if cookie, e := r.Cookie("musi_session"); e == nil {
			var id string
			if e = s.Accounts.DB.QueryRowContext(r.Context(), "SELECT student_id FROM student_session WHERE token_hash=? AND expires_at>UTC_TIMESTAMP()", tokenHash(cookie.Value)).Scan(&id); e != nil {
				failure(w, 401, "登录已过期，请重新登录")
				return
			}
			copy.StudentID = id
		}
		copy.Handler().ServeHTTP(w, r)
	})
}
