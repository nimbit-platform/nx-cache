package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	cookieName  = "nx_cache_session"
	sessionTTL  = 12 * time.Hour
	csrfPurpose = "nx-cache-ui"
)

func BearerOK(header, writeToken, readToken string, write bool) (ok bool, status int, msg string) {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return false, http.StatusUnauthorized, "Missing or invalid authentication token"
	}
	got := strings.TrimPrefix(header, prefix)
	if writeToken != "" && secureEq(got, writeToken) {
		return true, 0, ""
	}
	if readToken != "" && secureEq(got, readToken) {
		if write {
			return false, http.StatusForbidden, "Access forbidden. (e.g. read-only token used to write)"
		}
		return true, 0, ""
	}
	return false, http.StatusUnauthorized, "Missing or invalid authentication token"
}

func CheckPassword(got, want string) bool {
	gh := sha256.Sum256([]byte(got))
	wh := sha256.Sum256([]byte(want))
	return subtle.ConstantTimeCompare(gh[:], wh[:]) == 1
}

type Sessions struct {
	Secret   []byte
	Username string
	Secure   bool
}

func (s *Sessions) SetCookie(w http.ResponseWriter, username string) {
	exp := time.Now().Add(sessionTTL).Unix()
	payload := username + "|" + strconv.FormatInt(exp, 10)
	token := payload + "." + sign(s.Secret, payload)
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    base64.RawURLEncoding.EncodeToString([]byte(token)),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.Secure,
		Expires:  time.Unix(exp, 0),
		MaxAge:   int(sessionTTL.Seconds()),
	})
}

func (s *Sessions) ClearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.Secure,
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
	})
}

func (s *Sessions) UsernameFromRequest(r *http.Request) (string, bool) {
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" {
		return "", false
	}
	raw, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return "", false
	}
	token := string(raw)
	payload, sig, ok := strings.Cut(token, ".")
	if !ok || !secureEq(sig, sign(s.Secret, payload)) {
		return "", false
	}
	user, expStr, ok := strings.Cut(payload, "|")
	if !ok || user == "" || user != s.Username {
		return "", false
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return "", false
	}
	return user, true
}

func (s *Sessions) CSRFToken(username string) string {
	return sign(s.Secret, csrfPurpose+"|"+username)
}

func (s *Sessions) ValidCSRFToken(username, token string) bool {
	return token != "" && secureEq(token, s.CSRFToken(username))
}

func sign(secret []byte, payload string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func secureEq(a, b string) bool {
	ah := sha256.Sum256([]byte(a))
	bh := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ah[:], bh[:]) == 1
}
