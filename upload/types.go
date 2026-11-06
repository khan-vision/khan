package upload

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"image"
	_ "image/gif"  // image.DecodeConfig-д декодер бүртгэнэ
	_ "image/jpeg" // (stdlib-ийн init; манай init биш)
	_ "image/png"
	"io"
	"net/http"
	"strings"
)

// Файлын ангилал (хадгалах хавтасны нэр, URL-ийн эхний сегмент).
const (
	CategoryImages    = "images"
	CategoryDocuments = "documents"
	CategoryVideos    = "videos"
	CategoryAudios    = "audios"
)

// TypeRule — нэг ангилалд зөвшөөрөх өргөтгөл ба хэмжээний хязгаар.
type TypeRule struct {
	// Exts — зөвшөөрөх өргөтгөлүүд ("jpg", цэггүй, жижиг). Зөвхөн кодод агуулгын
	// шалгагчтай (`known`) өргөтгөл байж болно: шалгагчгүй өргөтгөл нэмэх боломжгүй.
	Exts []string
	// MaxBytes — энэ ангилалд зөвшөөрөх дээд хэмжээ; 0 бол Config.MaxBytes.
	// Үр дүнтэй хязгаар = min(MaxBytes, Config.MaxBytes).
	MaxBytes int64
}

// DefaultTypes — анхдагч бодлого. sharedlib (.so) г.м. гүйцэтгэгдэх төрөл,
// html/js/exe/svgz/swf/avi/dwg ЗОРИУД оруулаагүй. Зураг 20 МБ-аас том байж
// болохгүй (decode-ийн ачаалал); бусад нь Config.MaxBytes.
func DefaultTypes() map[string]TypeRule {
	return map[string]TypeRule{
		CategoryImages:    {Exts: []string{"jpg", "jpeg", "png", "gif", "webp", "svg"}, MaxBytes: 20 << 20},
		CategoryDocuments: {Exts: []string{"pdf", "doc", "docx", "xls", "xlsx", "ppt", "pptx", "csv", "zip", "json", "txt"}},
		CategoryVideos:    {Exts: []string{"mp4", "m4v", "webm"}},
		CategoryAudios:    {Exts: []string{"mp3", "wav", "aac", "m4a"}},
	}
}

// Content-ийн шалгалтын нөөц хэмжээ.
const (
	sniffLen     = 1024     // эхний байтаас магик унших
	textScanLen  = 64 << 10 // текстийн шалгалт
	svgMaxBytes  = 2 << 20  // SVG-г бүтнээр нь санах ойд уншина ⇒ 2 МБ
	maxZipEntry  = 20000    // zip central directory-н дээд бичлэг
	jsonScanSize = 32 << 20 // JSON-г урсгалаар шалгах дээд хэмжээ
)

// File — шалгалтад хэрэгтэй хураангуй: multipart.File ийм.
type File interface {
	io.Reader
	io.ReaderAt
	io.Seeker
}

// spec — өргөтгөл бүрийн агуулгын шалгагч.
type spec struct {
	category string
	mime     string
	check    func(head []byte, f File, size int64) error
}

var errContent = errors.New("файлын агуулга өргөтгөлтэй таарахгүй байна")

// known — шалгагчтай бүх өргөтгөл (allowlist-ийн дээд хил).
var known = map[string]spec{
	"jpg":  {CategoryImages, "image/jpeg", checkSniff("image/jpeg")},
	"jpeg": {CategoryImages, "image/jpeg", checkSniff("image/jpeg")},
	"png":  {CategoryImages, "image/png", checkSniff("image/png")},
	"gif":  {CategoryImages, "image/gif", checkSniff("image/gif")},
	"webp": {CategoryImages, "image/webp", checkSniff("image/webp")},
	"svg":  {CategoryImages, "image/svg+xml", checkSVG},

	"pdf":  {CategoryDocuments, "application/pdf", checkPDF},
	"doc":  {CategoryDocuments, "application/msword", checkOLE},
	"xls":  {CategoryDocuments, "application/vnd.ms-excel", checkOLE},
	"ppt":  {CategoryDocuments, "application/vnd.ms-powerpoint", checkOLE},
	"docx": {CategoryDocuments, "application/vnd.openxmlformats-officedocument.wordprocessingml.document", checkOOXML("word/document.xml")},
	"xlsx": {CategoryDocuments, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", checkOOXML("xl/workbook.xml")},
	"pptx": {CategoryDocuments, "application/vnd.openxmlformats-officedocument.presentationml.presentation", checkOOXML("ppt/presentation.xml")},
	"zip":  {CategoryDocuments, "application/zip", checkZip},
	"csv":  {CategoryDocuments, "text/csv; charset=utf-8", checkText},
	"txt":  {CategoryDocuments, "text/plain; charset=utf-8", checkText},
	"json": {CategoryDocuments, "application/json", checkJSON},

	"mp4":  {CategoryVideos, "video/mp4", checkFtyp},
	"m4v":  {CategoryVideos, "video/x-m4v", checkFtyp},
	"webm": {CategoryVideos, "video/webm", checkWebM},

	"mp3": {CategoryAudios, "audio/mpeg", checkMP3},
	"wav": {CategoryAudios, "audio/wav", checkWAV},
	"aac": {CategoryAudios, "audio/aac", checkADTS},
	"m4a": {CategoryAudios, "audio/mp4", checkFtyp},
}

// mimeOf — өргөтгөлийн (жижиг, цэггүй) MIME; олдохгүй бол "".
func mimeOf(ext string) string { return known[ext].mime }

// checkSniff — http.DetectContentType-ийн үр дүн яг want байх.
func checkSniff(want string) func([]byte, File, int64) error {
	return func(head []byte, _ File, _ int64) error {
		if http.DetectContentType(head) != want {
			return errContent
		}
		return nil
	}
}

func checkPDF(head []byte, _ File, _ int64) error {
	// Spec: "%PDF-" эхний 1024 байтад.
	if !bytes.Contains(head, []byte("%PDF-")) {
		return errContent
	}
	return nil
}

func checkOLE(head []byte, _ File, _ int64) error {
	if !bytes.HasPrefix(head, []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}) {
		return errContent
	}
	return nil
}

func openZip(head []byte, f File, size int64) (*zip.Reader, error) {
	if !bytes.HasPrefix(head, []byte("PK\x03\x04")) && !bytes.HasPrefix(head, []byte("PK\x05\x06")) {
		return nil, errContent
	}
	zr, err := zip.NewReader(f, size)
	if err != nil || len(zr.File) > maxZipEntry {
		return nil, errContent
	}
	return zr, nil
}

func checkZip(head []byte, f File, size int64) error {
	_, err := openZip(head, f, size)
	return err
}

// checkOOXML — zip + `[Content_Types].xml` + тухайн форматын гол part.
func checkOOXML(part string) func([]byte, File, int64) error {
	return func(head []byte, f File, size int64) error {
		zr, err := openZip(head, f, size)
		if err != nil {
			return err
		}
		var hasCT, hasPart bool
		for _, e := range zr.File {
			switch e.Name {
			case "[Content_Types].xml":
				hasCT = true
			case part:
				hasPart = true
			}
		}
		if !hasCT || !hasPart {
			return errContent
		}
		return nil
	}
}

func checkFtyp(head []byte, _ File, _ int64) error {
	if len(head) < 12 || string(head[4:8]) != "ftyp" {
		return errContent
	}
	return nil
}

func checkWebM(head []byte, _ File, _ int64) error {
	if !bytes.HasPrefix(head, []byte{0x1A, 0x45, 0xDF, 0xA3}) {
		return errContent
	}
	return nil
}

func checkMP3(head []byte, _ File, _ int64) error {
	if bytes.HasPrefix(head, []byte("ID3")) {
		return nil
	}
	// MPEG frame sync: 11 битийн 1.
	if len(head) >= 2 && head[0] == 0xFF && head[1]&0xE0 == 0xE0 {
		return nil
	}
	return errContent
}

func checkWAV(head []byte, _ File, _ int64) error {
	if len(head) < 12 || string(head[0:4]) != "RIFF" || string(head[8:12]) != "WAVE" {
		return errContent
	}
	return nil
}

func checkADTS(head []byte, _ File, _ int64) error {
	if len(head) >= 2 && head[0] == 0xFF && head[1]&0xF6 == 0xF0 {
		return nil
	}
	return errContent
}

// checkText — NUL/удирдах тэмдэгтгүй, HTML мэт танигдахгүй текст. CP1251 зэрэг
// UTF-8 биш кодчилолыг (Excel CSV) ЗӨВШӨӨРНӨ, гэхдээ ASCII control байж болохгүй.
func checkText(head []byte, f File, size int64) error {
	buf, err := readAt(f, size, textScanLen)
	if err != nil {
		return errContent
	}
	if !looksText(buf) {
		return errContent
	}
	if strings.HasPrefix(http.DetectContentType(buf), "text/html") {
		return errContent
	}
	return nil
}

func looksText(buf []byte) bool {
	for _, b := range buf {
		if b < 0x20 && b != '\t' && b != '\n' && b != '\r' && b != '\f' {
			return false
		}
		if b == 0x7F {
			return false
		}
	}
	return true
}

// checkJSON — текст + бүтэн синтаксис шалгалт (урсгалаар, санах ойд бүтнээр нь
// уншихгүй). UTF-8 BOM-ыг алгасна.
func checkJSON(_ []byte, f File, size int64) error {
	if size > jsonScanSize {
		return errContent
	}
	var off int64
	var bom [3]byte
	if n, _ := f.ReadAt(bom[:], 0); n == 3 && bom == [3]byte{0xEF, 0xBB, 0xBF} {
		off = 3
	}
	dec := json.NewDecoder(io.NewSectionReader(f, off, size-off))
	depth, tokens := 0, 0
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			if depth != 0 || tokens == 0 {
				return errContent // дутуу JSON
			}
			return nil
		}
		if err != nil {
			return errContent
		}
		tokens++
		if d, ok := tok.(json.Delim); ok {
			if d == '{' || d == '[' {
				depth++
			} else {
				depth--
			}
		}
	}
}

func readAt(f File, size, limit int64) ([]byte, error) {
	n := min(size, limit)
	buf := make([]byte, n)
	read, err := f.ReadAt(buf, 0)
	if err != nil && !(errors.Is(err, io.EOF) && int64(read) == n) {
		return nil, err
	}
	return buf[:read], nil
}

// checkSVG — SVG-г бүтнээр уншиж sanitizeSVG-ээр шалгана.
func checkSVG(_ []byte, f File, size int64) error {
	if size > svgMaxBytes {
		return errors.New("SVG файл хэт том байна")
	}
	data, err := readAt(f, size, svgMaxBytes)
	if err != nil {
		return errContent
	}
	return sanitizeSVG(data)
}

// maxPixels-ээс ихгүй эсэх, мөн бодит зураг эсэхийг шалгана (decode config).
// Өргөн/өндөр буцаана.
func imageConfig(f File, size int64, maxPixels int64) (w, h int, err error) {
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return 0, 0, errContent
	}
	head, err := readAt(f, size, 64)
	if err != nil {
		return 0, 0, errContent
	}
	if bytes.HasPrefix(head, []byte("RIFF")) {
		w, h, err = webpSize(head)
	} else {
		var cfg image.Config
		cfg, _, err = image.DecodeConfig(io.NewSectionReader(f, 0, size))
		w, h = cfg.Width, cfg.Height
	}
	if err != nil || w <= 0 || h <= 0 {
		return 0, 0, errContent
	}
	if int64(w)*int64(h) > maxPixels {
		return 0, 0, errTooManyPixels
	}
	return w, h, nil
}

var errTooManyPixels = errors.New("зургийн нягт (pixel) хэт их байна")

// webpSize — RIFF/WEBP-ийн VP8, VP8L, VP8X header-аас хэмжээ.
func webpSize(h []byte) (int, int, error) {
	if len(h) < 30 || string(h[8:12]) != "WEBP" {
		return 0, 0, errContent
	}
	switch string(h[12:16]) {
	case "VP8 ":
		if h[23] != 0x9d || h[24] != 0x01 || h[25] != 0x2a {
			return 0, 0, errContent
		}
		return int(binary.LittleEndian.Uint16(h[26:28]) & 0x3fff), int(binary.LittleEndian.Uint16(h[28:30]) & 0x3fff), nil
	case "VP8L":
		if h[20] != 0x2f {
			return 0, 0, errContent
		}
		b := binary.LittleEndian.Uint32(h[21:25])
		return int(b&0x3fff) + 1, int((b>>14)&0x3fff) + 1, nil
	case "VP8X":
		w := int(h[24]) | int(h[25])<<8 | int(h[26])<<16
		hh := int(h[27]) | int(h[28])<<8 | int(h[29])<<16
		return w + 1, hh + 1, nil
	}
	return 0, 0, errContent
}
