package main

import "github.com/jackc/pgx/v5/pgxpool"

type dbPool struct{ Pool *pgxpool.Pool }

func (d dbPool) Close() {
	if d.Pool != nil {
		d.Pool.Close()
	}
}
