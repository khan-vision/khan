// Package rediscache нь cache.Store ба cache.GenStore-ийг Redis (go-redis v9) дээр хэрэгжүүлнэ.
// Generation Redis-д байх тул олон instance бие биеийн bump-ийг ШУУД харна (процесс дотор memo хийхгүй).
//
//	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379"})
//	rc := rediscache.New(rdb)
//	c, err := cache.New(cache.Config{L2: rc}) // rc нь GenStore тул gens мөн Redis-д
//
// Redis-ийн тохиргоо: generation түлхүүр (`<prefix>:gen:*`) TTL-гүй тул `maxmemory-policy`-г
// `volatile-*` (эсвэл noeviction) болго — `allkeys-*` generation-ийг гаргавал тоолуур 0 болж, хуучин
// entry дахин «хүчинтэй» харагдах эрсдэлтэй. Entry нь TTL-тэй тул volatile-lru-д зөв шахагдана.
package rediscache

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// Store — Redis дээрх cache.Store + cache.GenStore. Client-ийг дуудагч эзэмшинэ (Close хийхгүй).
type Store struct {
	rdb redis.UniversalClient
}

// New — client (Client, ClusterClient, Ring, FailoverClient) дээр Store үүсгэнэ.
func New(rdb redis.UniversalClient) *Store { return &Store{rdb: rdb} }

// Get — GET. Байхгүй бол ok=false, алдаагүй.
func (s *Store) Get(ctx context.Context, key string) ([]byte, bool, error) {
	v, err := s.rdb.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return v, true, nil
}

// Set — SET key val PX <ms>. ttl<=0 бол хугацаагүй; ttl нь 1 мс-ээс богино бол 1 мс.
func (s *Store) Set(ctx context.Context, key string, val []byte, ttl time.Duration) error {
	if ttl <= 0 {
		return s.rdb.Set(ctx, key, val, 0).Err()
	}
	ms := ttl.Milliseconds()
	if ms < 1 {
		ms = 1
	}
	return s.rdb.Do(ctx, "SET", key, val, "PX", ms).Err()
}

// Gen — түлхүүр бүрийн generation (байхгүй бол 0). Нэг pipeline (cluster-д ч slot-оор зөв тархана).
func (s *Store) Gen(ctx context.Context, keys []string) ([]uint64, error) {
	out := make([]uint64, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	cmds := make([]*redis.StringCmd, len(keys))
	_, err := s.rdb.Pipelined(ctx, func(p redis.Pipeliner) error {
		for i, k := range keys {
			cmds[i] = p.Get(ctx, k)
		}
		return nil
	})
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, err
	}
	for i, cmd := range cmds {
		raw, err := cmd.Result()
		if errors.Is(err, redis.Nil) {
			continue
		}
		if err != nil {
			return nil, err
		}
		n, perr := strconv.ParseUint(raw, 10, 64)
		if perr != nil {
			return nil, fmt.Errorf("rediscache: generation %q буруу: %q", keys[i], raw)
		}
		out[i] = n
	}
	return out, nil
}

// Bump — түлхүүр бүрд INCR (түлхүүр тус бүр атомар; нэг pipeline-д).
func (s *Store) Bump(ctx context.Context, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	_, err := s.rdb.Pipelined(ctx, func(p redis.Pipeliner) error {
		for _, k := range keys {
			p.Incr(ctx, k)
		}
		return nil
	})
	return err
}
