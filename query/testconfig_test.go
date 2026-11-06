package query

import "github.com/gofiber/fiber/v3"

// noTimeout — app.Test-ийн хугацааны хязгааргүй тохиргоо (v2-ийн -1).
var noTimeout = fiber.TestConfig{Timeout: 0, FailOnTimeout: false}
