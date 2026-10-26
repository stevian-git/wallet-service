-- Local development bootstrap only.
--
-- ledger_migrator owns the database and schema and runs migrations.
-- ledger_app is used by the API and is deliberately never an object owner.
-- Both roles are provisioned by the platform (or this init script), not by
-- the migrations themselves.
CREATE ROLE ledger_migrator LOGIN PASSWORD 'migrator_dev_pw'
  NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT;

CREATE ROLE ledger_app LOGIN PASSWORD 'app_dev_pw'
  NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT;

-- docker-entrypoint-initdb.d runs against POSTGRES_DB = wallet.
ALTER DATABASE wallet OWNER TO ledger_migrator;
ALTER SCHEMA public OWNER TO ledger_migrator;

GRANT CONNECT ON DATABASE wallet TO ledger_migrator;
GRANT CONNECT ON DATABASE wallet TO ledger_app;
