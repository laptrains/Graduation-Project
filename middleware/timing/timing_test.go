package timing

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func TestTiming(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(Timing())
	r.GET("/api/v1/AI/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	out := captureLog(t, func() {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/AI/test", nil)
		r.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("middleware should not break response, got %d", w.Code)
		}
	})

	for _, want := range []string{"[timing]", "method=GET", "path=/api/v1/AI/test", "status=200", "cost="} {
		if !strings.Contains(out, want) {
			t.Fatalf("log missing %q: %s", want, out)
		}
	}
	if strings.Contains(out, "[SLOW]") {
		t.Fatalf("fast request should not be marked SLOW: %s", out)
	}
}

func TestTimingSlow(t *testing.T) {
	old := slowThreshold
	slowThreshold = 0
	defer func() { slowThreshold = old }()

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(Timing())
	// 未匹配路由时回退为请求路径
	r.POST("/api/v1/AI/chat/send", func(c *gin.Context) {
		time.Sleep(time.Millisecond)
		c.JSON(http.StatusInternalServerError, gin.H{})
	})

	out := captureLog(t, func() {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/AI/chat/send", nil)
		r.ServeHTTP(w, req)

		if w.Code != http.StatusInternalServerError {
			t.Fatalf("unexpected code %d", w.Code)
		}
	})

	for _, want := range []string{"[timing][SLOW]", "method=POST", "status=500"} {
		if !strings.Contains(out, want) {
			t.Fatalf("log missing %q: %s", want, out)
		}
	}
}

// captureLog 临时接管 log 输出并返回本段代码产生的日志
func captureLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)

	fn()
	return buf.String()
}
