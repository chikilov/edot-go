// SPDX-License-Identifier: Apache-2.0
package edotgo

import (
	"database/sql"
	"database/sql/driver"

	"github.com/XSAM/otelsql"
)

// OpenSQLDB opens an instrumented database/sql pool from a driver connector.
// The caller owns the pool. SQL text and driver error messages are omitted
// from spans because they can contain application data.
func (s *SDK) OpenSQLDB(connector driver.Connector) *sql.DB {
	return otelsql.OpenDB(connector,
		otelsql.WithTracerProvider(s.TracerProvider),
		otelsql.WithMeterProvider(s.MeterProvider),
		otelsql.WithSpanOptions(otelsql.SpanOptions{
			DisableQuery: true,
			RecordError:  func(error) bool { return false },
		}),
	)
}
