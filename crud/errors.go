package crud

import (
	"errors"
	"net/http"

	"github.com/gofiber/fiber/v3"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/khan-vision/khan/db"
	"github.com/khan-vision/khan/obs"
	"github.com/khan-vision/khan/validate"
)

// errNotFound — мөр байхгүй ЭСВЭЛ өөр org-ийнх (ялгахгүй). Hook-ийн доторх gorm.ErrRecordNotFound
// нь энэ биш (hook-ийн алдаа → 500); hook 404 хүсвэл crud.NotFound(...) буцаана.
var errNotFound = errors.New("khan/crud: мөр олдсонгүй")

// Error — hook-оос тодорхой HTTP status-той алдаа буцаах. Msg нь хэрэглэгчид шууд
// харагдана (Монгол, дотоод дэлгэрэнгүйгүй).
type Error struct {
	Status int
	Msg    string
}

func (e *Error) Error() string { return e.Msg }

// Conflict — 409.
func Conflict(msg string) error { return &Error{http.StatusConflict, msg} }

// Forbidden — 403.
func Forbidden(msg string) error { return &Error{http.StatusForbidden, msg} }

// BadRequest — 400.
func BadRequest(msg string) error { return &Error{http.StatusBadRequest, msg} }

// NotFound — 404.
func NotFound(msg string) error { return &Error{http.StatusNotFound, msg} }

const (
	msgForbidden = "Хандах эрхгүй байна"
	msgInternal  = "Дотоод алдаа гарлаа"
	msgBadJSON   = "Хүсэлтийн JSON буруу"
	msgTooLarge  = "Хүсэлтийн хэмжээ хэт том"
	msgBadID     = "id буруу"
	msgVersion   = "Мэдээлэл өөр хэрэглэгчээр өөрчлөгдсөн байна. Дахин ачаалж шалгана уу"
	msgUnique    = "Ийм утгатай мөр аль хэдийн байна"
	msgFK        = "Холбоотой өгөгдөл байгаа тул үйлдлийг гүйцэтгэх боломжгүй"
	msgTooBig    = "Утга хэт урт эсвэл хэт том байна"
	msgRetry     = "Түр зуурын зөрчил гарлаа, дахин оролдоно уу"
)

func jsonErr(c fiber.Ctx, status int, msg string) error {
	return c.Status(status).JSON(fiber.Map{"error": msg})
}

// fail — алдааг HTTP хариу болгоно. Жинхэнэ (дотоод) алдаа ХЭЗЭЭ Ч body-д орохгүй:
// 500 + ерөнхий мессеж, дэлгэрэнгүй нь obs logger-т.
func (e *engine) fail(c fiber.Ctx, action Action, err error) error {
	var ve validate.Errors
	var ce *Error
	var pg *pgconn.PgError
	switch {
	case errors.As(err, &ve):
		return validate.Write(c, ve)
	case errors.As(err, &ce):
		return jsonErr(c, ce.Status, ce.Msg)
	case errors.Is(err, db.ErrConflict):
		return jsonErr(c, fiber.StatusConflict, msgVersion)
	case errors.Is(err, errNotFound):
		return jsonErr(c, fiber.StatusNotFound, e.label+" олдсонгүй")
	case errors.As(err, &pg):
		switch pg.Code {
		case "23505": // unique_violation
			return jsonErr(c, fiber.StatusConflict, msgUnique)
		case "23503": // foreign_key_violation
			return jsonErr(c, fiber.StatusConflict, msgFK)
		case "40001", "40P01", "55P03": // serialization, deadlock, lock_not_available
			return jsonErr(c, fiber.StatusConflict, msgRetry)
		case "22001", "22003": // string_data_right_truncation, numeric_value_out_of_range
			return jsonErr(c, fiber.StatusUnprocessableEntity, msgTooBig)
		}
	}
	obs.With(c).Error("crud: дотоод алдаа", "resource", e.name, "action", string(action), "err", err)
	return jsonErr(c, fiber.StatusInternalServerError, msgInternal)
}
