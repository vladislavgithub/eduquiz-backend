package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func setupRouter(iss *Issuer, h gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/p", RequireAuth(iss), h)
	r.GET("/teacher", RequireAuth(iss), RequireRole("teacher"), h)
	return r
}

func TestRequireAuth_AcceptsValidToken(t *testing.T) {
	iss := NewIssuer("s", time.Minute, time.Hour)
	uid := uuid.New()
	pair, _ := iss.IssuePair(uid, "teacher")

	r := setupRouter(iss, func(c *gin.Context) {
		got, ok := UserIDFromContext(c)
		if !ok || got != uid {
			t.Errorf("UserIDFromContext: got=%v ok=%v", got, ok)
		}
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/p", nil)
	req.Header.Set("Authorization", "Bearer "+pair.Access)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}

func TestRequireAuth_RejectsMissingHeader(t *testing.T) {
	iss := NewIssuer("s", time.Minute, time.Hour)
	r := setupRouter(iss, func(c *gin.Context) { c.Status(http.StatusOK) })
	req := httptest.NewRequest(http.MethodGet, "/p", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestRequireAuth_RejectsRefreshTokenOnAccessRoute(t *testing.T) {
	iss := NewIssuer("s", time.Minute, time.Hour)
	pair, _ := iss.IssuePair(uuid.New(), "student")

	r := setupRouter(iss, func(c *gin.Context) { c.Status(http.StatusOK) })
	req := httptest.NewRequest(http.MethodGet, "/p", nil)
	req.Header.Set("Authorization", "Bearer "+pair.Refresh)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (refresh used as access)", w.Code)
	}
}

func TestRequireRole_AllowsAndRejects(t *testing.T) {
	iss := NewIssuer("s", time.Minute, time.Hour)

	teacherPair, _ := iss.IssuePair(uuid.New(), "teacher")
	studentPair, _ := iss.IssuePair(uuid.New(), "student")

	r := setupRouter(iss, func(c *gin.Context) { c.Status(http.StatusOK) })

	// teacher — допустим.
	req := httptest.NewRequest(http.MethodGet, "/teacher", nil)
	req.Header.Set("Authorization", "Bearer "+teacherPair.Access)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("teacher: status = %d, want 200", w.Code)
	}

	// student — 403.
	req = httptest.NewRequest(http.MethodGet, "/teacher", nil)
	req.Header.Set("Authorization", "Bearer "+studentPair.Access)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("student: status = %d, want 403", w.Code)
	}
}
