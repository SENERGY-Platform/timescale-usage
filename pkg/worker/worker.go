/*
 *    Copyright 2023 InfAI (CC SES)
 *
 *    Licensed under the Apache License, Version 2.0 (the "License");
 *    you may not use this file except in compliance with the License.
 *    You may obtain a copy of the License at
 *
 *        http://www.apache.org/licenses/LICENSE-2.0
 *
 *    Unless required by applicable law or agreed to in writing, software
 *    distributed under the License is distributed on an "AS IS" BASIS,
 *    WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 *    See the License for the specific language governing permissions and
 *    limitations under the License.
 */

package worker

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/SENERGY-Platform/timescale-usage/pkg/configuration"
	"github.com/jackc/pgx"
	"github.com/jackc/pgx/pgtype"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

type Worker struct {
	conn         *pgx.ConnPool
	config       configuration.Config
	bytesMetrics *prometheus.GaugeVec
}

func Start(ctx context.Context, config configuration.Config) error {
	conn, err := pgx.NewConnPool(pgx.ConnPoolConfig{
		ConnConfig: pgx.ConnConfig{
			Host:     config.PostgresHost,
			Port:     config.PostgresPort,
			Database: config.PostgresDb,
			User:     config.PostgresUser,
			Password: config.PostgresPw,
		},
		MaxConnections: 10,
		AcquireTimeout: 0})
	if err != nil {
		return err
	}
	defer conn.Close()

	bytesMetrics := promauto.NewGaugeVec(prometheus.GaugeOpts{Name: "timescale_table_size_bytes", Help: "Table size in bytes"}, []string{"table"})

	w := &Worker{conn: conn, config: config, bytesMetrics: bytesMetrics}
	err = w.migrate()
	if err != nil {
		return err
	}

	if len(config.Duration) == 0 {
		return w.run()
	}

	d, err := time.ParseDuration(config.Duration)
	if err != nil {
		return err
	}

	ticker := time.NewTicker(d) // start ticker early, since run() takes some time

	err = w.run() // run once at startup
	if err != nil {
		return err
	}

	for {
		select {
		case <-ticker.C:
			err = w.run()
			if err != nil {
				return err
			}
		case <-ctx.Done():
			return nil
		}
	}
}

func (w *Worker) run() (err error) {
	log.Println("Starting Update..")
	err = w.upsertTables()
	if err != nil {
		return err
	}

	err = w.upsertViews()
	if err != nil {
		return err
	}

	// Cleanup outdated
	log.Println("Cleanup")
	_, err = w.conn.Exec(fmt.Sprintf("DELETE FROM %v.usage where \"table\" NOT IN (SELECT hypertable_name FROM timescaledb_information.hypertables  WHERE hypertable_schema = '%v') AND \"table\" NOT IN (SELECT view_name FROM timescaledb_information.continuous_aggregates WHERE view_schema = '%v');", w.config.PostgresUsageSchema, w.config.PostgresSourceSchema, w.config.PostgresSourceSchema))
	if err != nil {
		return err
	}

	log.Println("Done")
	return nil
}

func (w *Worker) upsertTables() error {
	return w.upsertWithQuery("SELECT hypertable_schema, hypertable_name, hypertable_approximate_size(format('%I.%I', hypertable_schema, hypertable_name)::regclass)  FROM timescaledb_information.hypertables;")
}

func (w *Worker) upsertViews() error {
	return w.upsertWithQuery("SELECT view_schema, view_name, hypertable_approximate_size(format('%I.%I', view_schema, view_name)::regclass) FROM timescaledb_information.continuous_aggregates;")
}

func (w *Worker) upsertWithQuery(query string) error {
	rows, err := w.conn.Query(query)
	if err != nil {
		return err
	}
	for rows.Next() {
		var size pgtype.Int8
		var schema, table string
		err = rows.Scan(&schema, &table, &size)
		if err != nil {
			return err
		}
		err = w.upsert(schema, table, size)
		if err != nil {
			if errIsTableDoesNotExist(err) {
				log.Println("WARNING: Table " + table + " seems to no longer exist")
				continue
			}
			return err
		}
	}
	return nil
}

func (w *Worker) upsert(schema string, table string, size pgtype.Int8) (err error) {
	now := time.Now()

	var tableSizeBytes int64 = 0
	if size.Get() != nil {
		tableSizeBytes = size.Get().(int64)
	}

	firstDate := now
	pgdate := pgtype.Timestamptz{}
	err = w.conn.QueryRow("SELECT time from \"" + schema + "\".\"" + table + "\" ORDER BY time ASC LIMIT 1;").Scan(&pgdate)
	if err != nil && err != pgx.ErrNoRows {
		return err
	}
	if err == nil {
		firstDate = pgdate.Get().(time.Time)
	}
	days := now.Sub(firstDate).Hours() / 24

	var bytesPerDay float64 = 0
	if days != 0 {
		bytesPerDay = float64(tableSizeBytes) / days
	}

	log.Printf("%v %v %v\n", table, tableSizeBytes, bytesPerDay)

	nowStr := now.Format(time.RFC3339)
	query := fmt.Sprintf("INSERT INTO %v.usage (\"table\", bytes, updated_at, bytes_per_day) VALUES ('%v', %v, '%v', %v) ON CONFLICT (\"table\") DO UPDATE SET bytes = %v, updated_at = '%v', bytes_per_day = %v;", w.config.PostgresUsageSchema, table, tableSizeBytes, nowStr, bytesPerDay, tableSizeBytes, nowStr, bytesPerDay)
	_, err = w.conn.Exec(query)
	if err != nil {
		return err
	}

	w.bytesMetrics.WithLabelValues(table).Set(float64(tableSizeBytes))

	return nil
}

func errIsTableDoesNotExist(err error) bool {
	return strings.Contains(err.Error(), "SQLSTATE 42P01")
}
