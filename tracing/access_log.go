package tracing

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"runtime/debug"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const maxAccessLogBodyBytes = 64 * 1024 // 64KB，防止大请求体撑爆内存

var accessLogSensitiveKeys = map[string]bool{
	"password": true, "token": true, "access_token": true,
	"secret": true, "api_key": true, "api_secret": true, "private_key": true,
}

type accessLogWriter struct {
	gin.ResponseWriter
	body *bytes.Buffer
}

func (w *accessLogWriter) Write(b []byte) (int, error) {
	return w.body.Write(b)
}

func (w *accessLogWriter) WriteString(s string) (int, error) {
	return w.body.WriteString(s)
}

// AccessLogMiddleware 是四服务统一使用的入口中间件：提取/生成追踪标识（委托 GinMiddleware），
// 缓冲响应体注入 traceId/code/message 默认值，输出结构化访问日志（错误响应额外记录响应体，
// 5xx 同时记录脱敏后的请求体，/health 探针跳过），并在 panic 时恢复、记录堆栈、返回含
// traceId 的错误响应。取代此前四服务各自独立维护的 TraceMiddleware + GinCustomRecovery
// 两个中间件组合。
//
// panic 恢复与响应缓冲/flush 必须在同一个函数调用栈里完成——recover 发生的位置必须比
// flush 更深（即 flush 所在的调用帧要在 recover 之后仍能继续往下执行），否则 recover
// 写的响应体会永远留在缓冲区里出不去。四服务历史上把这两部分拆成两个独立注册的中间件，
// 注册顺序一旦写反就会复现这个问题（017 traceId 全链路项目实测复现过一次，修复方式是
// 调整注册顺序）；折进同一个函数、不依赖外部注册顺序，才是这类问题的正确修复层级。
func AccessLogMiddleware(logger *zap.Logger) gin.HandlerFunc {
	ginTrace := GinMiddleware()
	return func(c *gin.Context) {
		// 读取请求体（限 64KB），读完后还原，让 handler 照常使用。
		// multipart/form-data 请求体含二进制文件，不读取以避免截断大文件。
		var reqBody []byte
		ct := c.Request.Header.Get("Content-Type")
		isMultipart := len(ct) >= 9 && ct[:9] == "multipart"
		if c.Request.Body != nil && !isMultipart {
			remaining := c.Request.Body
			reqBody, _ = io.ReadAll(io.LimitReader(remaining, maxAccessLogBodyBytes))
			c.Request.Body = io.NopCloser(io.MultiReader(bytes.NewReader(reqBody), remaining))
		}

		w := &accessLogWriter{ResponseWriter: c.Writer, body: &bytes.Buffer{}}
		c.Writer = w

		start := time.Now()
		func() {
			defer func() {
				if r := recover(); r != nil {
					traceID := FromContext(c.Request.Context())
					logger.Error("panic_recovered",
						zap.Any("error", r),
						zap.String("traceId", traceID),
						zap.String("method", c.Request.Method),
						zap.String("path", c.Request.URL.Path),
						zap.ByteString("stack", debug.Stack()),
					)
					c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
						"code":    50001,
						"message": "internal server error",
						"data":    gin.H{},
						"traceId": traceID,
					})
				}
			}()
			ginTrace(c)
		}()

		traceID := c.GetString(TraceIDKey)
		flushAccessLogBody(w, traceID)

		// 健康检查探针不记日志
		if c.Request.URL.Path == "/health" {
			return
		}

		status := c.Writer.Status()
		fields := []zap.Field{
			zap.String("traceId", traceID),
			zap.String("method", c.Request.Method),
			zap.String("path", c.Request.URL.Path),
			zap.Int("status", status),
			zap.Int64("duration_ms", time.Since(start).Milliseconds()),
			zap.String("ip", c.ClientIP()),
		}
		if status >= 400 {
			fields = append(fields, zap.String("resp_body", w.body.String()))
			if status >= 500 && len(reqBody) > 0 {
				fields = append(fields, zap.String("req_body", sanitizeAccessLogBody(reqBody)))
			}
			logger.Error("request_error", fields...)
		} else {
			logger.Info("request", fields...)
		}
	}
}

// flushAccessLogBody 将缓冲的响应体规范化后写出：
//   - 注入 traceId（始终覆盖）
//   - 注入 code 默认值（仅当 handler 未设置时：2xx → 0，4xx/5xx → status*100+1）
//   - 注入 message 默认值（仅当 handler 未设置时：2xx → "ok"，4xx/5xx → HTTP 状态文本）
//
// 非 JSON 对象（数组、纯文本、CSV 等）原样写出，不做任何注入。
func flushAccessLogBody(w *accessLogWriter, traceID string) {
	body := w.body.Bytes()
	if len(body) > 0 && body[0] == '{' {
		var m map[string]interface{}
		if json.Unmarshal(body, &m) == nil {
			status := w.ResponseWriter.Status()

			m["traceId"] = traceID

			if _, ok := m["code"]; !ok {
				if status < 400 {
					m["code"] = 0
				} else {
					m["code"] = status*100 + 1
				}
			}

			if _, ok := m["message"]; !ok {
				if status < 400 {
					m["message"] = "ok"
				} else {
					m["message"] = http.StatusText(status)
				}
			}

			if newBody, err := json.Marshal(m); err == nil {
				w.ResponseWriter.Header().Set("Content-Length", strconv.Itoa(len(newBody)))
				w.ResponseWriter.Write(newBody)
				return
			}
		}
	}
	w.ResponseWriter.Write(body)
}

// sanitizeAccessLogBody 将请求体 JSON 中的敏感字段替换为 "***"，非 JSON 原样返回。
func sanitizeAccessLogBody(body []byte) string {
	var m map[string]interface{}
	if json.Unmarshal(body, &m) != nil {
		return string(body)
	}
	for k := range m {
		if accessLogSensitiveKeys[k] {
			m[k] = "***"
		}
	}
	b, _ := json.Marshal(m)
	return string(b)
}
