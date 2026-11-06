package cache

import (
	"context"
	"strings"

	"github.com/khan-vision/khan/events"
)

// BumpOnEvents — `events.Dispatcher.Handle("finance.*", cache.BumpOnEvents(c))` хэлбэрээр бүртгэнэ.
// Topic `<resource>.<action>` (`finance.invoice.posted` → resource `finance.invoice`): сүүлийн
// сегментийг хасаад Bump(org, resource) хийнэ. org-гүй (NULL) event — системийн мөр / глобал / single-tenant
// (crud нь ийм event-д OrgID-г хоосон тавьдаг) — crud-ийн `_system` ба `_global` namespace-ийг bump хийнэ.
// Цэггүй topic-ийг алгасна. Bump алдвал алдааг буцаана — Dispatcher дахин оролдоно (bump нь idempotent).
func BumpOnEvents(c *Cache) events.Handler {
	return func(ctx context.Context, e events.Delivered) error {
		i := strings.LastIndexByte(e.Topic, '.')
		if i <= 0 {
			c.logf("cache: event алгасав topic=%q org=%q", e.Topic, e.OrgID)
			return nil
		}
		if e.OrgID == "" {
			// org-гүй (системийн мөр / глобал / single-tenant) event: crud-ийн `_system` ба `_global`
			// namespace-ийн generation-ийг bump хийнэ (аль нь ч уншигдаагүй бол илүү bump хор хөнөөлгүй).
			if err := c.Bump(ctx, "_system", e.Topic[:i]); err != nil {
				return err
			}
			return c.Bump(ctx, "_global", e.Topic[:i])
		}
		return c.Bump(ctx, e.OrgID, e.Topic[:i])
	}
}
