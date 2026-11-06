package obs

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/gofiber/fiber/v3"
	"gorm.io/gorm"
)

// Check — нэг хамаарлын (DB, redis …) эрүүл мэндийн шалгалт; nil = эрүүл.
type Check func(ctx context.Context) error

// DBCheck — gorm.DB-ийн доорх *sql.DB-г PingContext-ээр шалгана.
func DBCheck(gdb *gorm.DB) Check {
	return func(ctx context.Context) error {
		sqlDB, err := gdb.DB()
		if err != nil {
			return err
		}
		return sqlDB.PingContext(ctx)
	}
}

// HealthOption — Health-ийн сонголт.
type HealthOption func(*healthOptions)

type healthOptions struct {
	timeout time.Duration
	details bool
}

// WithCheckTimeout — шалгалт бүрийн хугацааны хязгаар (анхдагч 2с).
func WithCheckTimeout(d time.Duration) HealthOption {
	return func(o *healthOptions) { o.timeout = d }
}

// WithErrorDetails — /readyz-ийн хариунд алдааны текстийг (redact хийсэн) оруулна.
// Анхдагчаар ОРУУЛАХГҮЙ: endpoint нийтэд нээлттэй, алдаа нь host/DSN зэрэг агуулж болно.
func WithErrorDetails() HealthOption {
	return func(o *healthOptions) { o.details = true }
}

type checkResult struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// Health — GET /healthz (процесс амьд: үргэлж 200 `{"status":"ok"}`) болон
// GET /readyz (checks-ийг зэрэг ажиллуулна; бүгд ok бол 200, үгүй бол 503, хариунд
// шалгалт бүрийн үр дүн) бүртгэнэ.
func Health(r fiber.Router, checks map[string]Check, opts ...HealthOption) {
	o := healthOptions{timeout: 2 * time.Second}
	for _, fn := range opts {
		fn(&o)
	}
	r.Get("/healthz", func(c fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "ok"})
	})
	r.Get("/readyz", func(c fiber.Ctx) error {
		results := runChecks(c.Context(), checks, o)
		status, code := "ok", fiber.StatusOK
		for name, res := range results {
			if res.Status != "ok" {
				status, code = "unavailable", fiber.StatusServiceUnavailable
				From(c.Context()).Warn("readyz: шалгалт амжилтгүй", "check", name, "error", res.Error)
			}
		}
		return c.Status(code).JSON(fiber.Map{"status": status, "checks": results})
	})
}

func runChecks(parent context.Context, checks map[string]Check, o healthOptions) map[string]checkResult {
	results := make(map[string]checkResult, len(checks))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for name, check := range checks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res := checkResult{Status: "ok"}
			if err := runOne(parent, check, o.timeout); err != nil {
				res.Status = "fail"
				if o.details {
					res.Error = RedactString(err.Error())
				} else {
					res.Error = "алдаа"
				}
			}
			mu.Lock()
			results[name] = res
			mu.Unlock()
		}()
	}
	wg.Wait()
	return results
}

// runOne — timeout-той, panic-ийг алдаа болгоно. Check ctx-ийг үл хүндэтгэвэл
// ч timeout дээр буцна (goroutine түүнийг дуустал үлдэж болзошгүй).
func runOne(parent context.Context, check Check, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		defer func() {
			if rec := recover(); rec != nil {
				done <- fmt.Errorf("panic: %v", rec)
			}
		}()
		done <- check(ctx)
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
