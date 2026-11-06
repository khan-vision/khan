package cache

import (
	"context"
	"strings"

	"github.com/khan-vision/khan/events"
)

// BumpOnEvents — `events.Dispatcher.Handle("finance.*", cache.BumpOnEvents(c))` хэлбэрээр бүртгэнэ.
// Topic `<resource>.<action>` (`finance.invoice.posted` → resource `finance.invoice`): сүүлийн
// сегментийг хасаад Bump(org, resource) хийнэ. org-гүй (NULL) эсвэл цэггүй topic-ийг алгасна
// (bump хийх зүйл байхгүй). Bump алдвал алдааг буцаана — Dispatcher дахин оролдоно (bump нь idempotent).
func BumpOnEvents(c *Cache) events.Handler {
	return func(ctx context.Context, e events.Delivered) error {
		i := strings.LastIndexByte(e.Topic, '.')
		if i <= 0 || e.OrgID == "" {
			c.logf("cache: event алгасав topic=%q org=%q", e.Topic, e.OrgID)
			return nil
		}
		return c.Bump(ctx, e.OrgID, e.Topic[:i])
	}
}
