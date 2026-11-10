package database

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
)

const postgresEmptySQL = `DO $$
DECLARE s text;
BEGIN
  FOR s IN SELECT nspname FROM pg_namespace
           WHERE nspname NOT IN ('pg_catalog', 'information_schema', 'pg_toast')
             AND nspname NOT LIKE 'pg\_temp\_%'
             AND nspname NOT LIKE 'pg\_toast\_temp\_%'
  LOOP
    EXECUTE format('DROP SCHEMA %I CASCADE', s);
  END LOOP;
END $$;
CREATE SCHEMA public;
`

const mysqlListDropsSQL = "SELECT CONCAT('DROP ', IF(TABLE_TYPE = 'VIEW', 'VIEW', 'TABLE'), ' IF EXISTS `', REPLACE(TABLE_NAME, '`', '``'), '`;') " +
	"FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() " +
	"UNION ALL SELECT CONCAT('DROP ', ROUTINE_TYPE, ' IF EXISTS `', REPLACE(ROUTINE_NAME, '`', '``'), '`;') " +
	"FROM information_schema.ROUTINES WHERE ROUTINE_SCHEMA = DATABASE() " +
	"UNION ALL SELECT CONCAT('DROP EVENT IF EXISTS `', REPLACE(EVENT_NAME, '`', '``'), '`;') " +
	"FROM information_schema.EVENTS WHERE EVENT_SCHEMA = DATABASE()"

// emptyPostgres drops every user schema, extensions in them included. The
// managed role is the database owner and a superuser, so nothing is out of reach.
func (r *Router) emptyPostgres(ctx context.Context, containerName, dbName string) error {
	if err := r.psqlStdin(ctx, containerName, dbName, strings.NewReader(postgresEmptySQL)); err != nil {
		return fmt.Errorf("empty database: %w", err)
	}
	return nil
}

// emptyMysql drops objects one by one rather than DROP DATABASE: the app login
// only holds privileges on the schema, and dropping the schema itself would
// discard its charset/collation defaults.
func (r *Router) emptyMysql(ctx context.Context, containerName, dbName string, c engineCreds) error {
	env := []string{"MYSQL_PWD=" + c.pass}
	drops, err := r.execOutput(ctx, containerName,
		[]string{"mysql", "-N", "-B", "--raw", "-u", c.user, dbName, "-e", mysqlListDropsSQL}, env)
	if err != nil {
		return fmt.Errorf("list objects to drop: %w", err)
	}
	if len(bytes.TrimSpace(drops)) == 0 {
		return nil
	}
	script := io.MultiReader(strings.NewReader("SET FOREIGN_KEY_CHECKS=0;\n"), bytes.NewReader(drops))
	if err := r.execWithStdin(ctx, containerName, []string{"mysql", "-u", c.user, dbName}, env, script); err != nil {
		return fmt.Errorf("empty database: %w", err)
	}
	return nil
}
