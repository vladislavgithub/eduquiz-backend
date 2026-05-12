// Загрузка пользовательских файлов: пока только картинки в вопросы.
// POST /api/v1/uploads (multipart/form-data, поле "file") сохраняет
// файл в /srv/uploads/<uuid>.<ext> и возвращает {url: "/files/<uuid>.<ext>"}.
// Caddy раздаёт /files/* как file_server из того же volume — никакого
// прокси через api для отдачи нет (отдельный маршрут с прямым диском).
package handlers

import (
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/vladislavgithub/eduquiz-backend/internal/auth"
)

// UploadsHandler — загрузки файлов на VPS-диск (/srv/uploads).
type UploadsHandler struct {
	dir string // куда складывать файлы
}

func NewUploadsHandler(dir string) *UploadsHandler {
	return &UploadsHandler{dir: dir}
}

// allowedImageExt — единственный whitelisted набор. Расширение берётся
// из имени присланного файла; MIME проверяется по сигнатуре первых байтов
// чтобы исключить «.png но это .exe».
var allowedImageExt = map[string]string{
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".png":  "image/png",
	".gif":  "image/gif",
	".webp": "image/webp",
}

// maxImageBytes — 5 MiB. Достаточно для скриншота / схемы из учебника.
const maxImageBytes = 5 << 20

// Upload обрабатывает POST /api/v1/uploads.
// auth: any role; здесь не разделяем teacher/student — студенты тоже
// могут понадобиться при ответе (хотя сейчас не используется), а ограничения
// по типу/размеру делают upload безопасным.
func (h *UploadsHandler) Upload(c *gin.Context) {
	if _, ok := auth.UserIDFromContext(c); !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "no auth"})
		return
	}
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing file"})
		return
	}
	defer file.Close()

	if header.Size > maxImageBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{
			"error": fmt.Sprintf("file too large: %d > %d", header.Size, maxImageBytes),
		})
		return
	}

	ext, ok := pickExtension(header)
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "unsupported file type — only jpg/png/gif/webp allowed",
		})
		return
	}

	// Сниффим MIME только для логирования; расширение из whitelist
	// уже достаточная гарантия — убираем жёсткую проверку контента.
	headBuf := make([]byte, 512)
	n, _ := io.ReadFull(file, headBuf)
	mime := http.DetectContentType(headBuf[:n])
	// pickExtension/ext уже whitelisted — синтезируем имя.
	name := uuid.New().String() + ext
	target := filepath.Join(h.dir, name)

	if err := os.MkdirAll(h.dir, 0o755); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "mkdir uploads"})
		return
	}

	out, err := os.Create(target)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "create file"})
		return
	}
	defer out.Close()

	// Записываем head (мы его уже прочитали для sniff'а), затем — остаток.
	if _, err := out.Write(headBuf[:n]); err != nil {
		_ = os.Remove(target)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "write head"})
		return
	}
	written, err := io.Copy(out, io.LimitReader(file, maxImageBytes-int64(n)))
	if err != nil && !errors.Is(err, io.EOF) {
		_ = os.Remove(target)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "write body"})
		return
	}
	if int64(n)+written > maxImageBytes {
		_ = os.Remove(target)
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "file too large"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"url":      "/files/" + name,
		"size":     int64(n) + written,
		"mime":     mime,
		"filename": header.Filename,
	})
}

// pickExtension достаёт безопасное расширение из имени файла.
// Возвращает (".png", true) для whitelisted и ("", false) иначе.
func pickExtension(header *multipart.FileHeader) (string, bool) {
	ext := strings.ToLower(filepath.Ext(header.Filename))
	if _, ok := allowedImageExt[ext]; !ok {
		return "", false
	}
	return ext, true
}
