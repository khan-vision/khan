package db

import (
	"context"
	"database/sql/driver"
	"errors"
)

// nopConnector — сүлжээгүй sql.DB үүсгэх (pool тохиргоог шалгахад).
type nopConnector struct{}

func (nopConnector) Connect(context.Context) (driver.Conn, error) {
	return nil, errors.New("nop connector")
}
func (nopConnector) Driver() driver.Driver { return nopDriver{} }

type nopDriver struct{}

func (nopDriver) Open(string) (driver.Conn, error) { return nil, errors.New("nop driver") }
