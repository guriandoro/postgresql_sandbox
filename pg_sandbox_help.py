def print_general_help():
    print("""Usage:
    pg_sandbox COMMAND [OPTIONS] SUBCOMMAND [POSITIONAL_ARGUMENTS]

Commands:
    build              compile PostgreSQL for the given major.minor version (as first argument after "build")
    cluster            provision/inspect/destroy a primary + N standby cluster ("cluster deploy|status|destroy")
    deploy             initialize the PostgreSQL instance, and start it (or attach as a standby with --replicate-from)
    destroy            stop the PostgreSQL instance, and delete all directories
    global_status      list every sandbox under PGS_ROOT_DIR with its running/stopped state and role
    help               print this message and exit
    promote            promote a standby sandbox to a standalone primary
    report             generate pg_gather report from out.txt output file.
    restart            restart the PostgreSQL instance
    run                runs the binary specified by the subcommand with the positional arguments
    setenv             write variables to the environment variables file
    start              start the PostgreSQL instance
    status             show the PostgreSQL instance status (incl. replication info when running)
    stop               stop the PostgreSQL instance
    use                run the psql client. All arguments after "use" are sent directly to psql

Options:
    -b, --bin          PostgreSQL binary directory to use with the sandbox environment
    -d, --dbname       database name (default: postgres)
    -D, --datadir      data directory to use within the sandbox directory (default: data)
    -f, --force        assume yes (when a confirmation prompt would be issued)
    -?, --help         print this message and exit
    -h, --host         hostname or IP address to use with commands (default: 127.0.0.1)
    -l, --log          PostgreSQL server log file (default: server.log)
    -n, --no-dsn       Don't add DSN (host, port and user) to the binary used with the "run" command
    -p, --port         port to use with commands (default: 65432)
    -s, --sandbox-dir  directory used as base for the needed files
    -U, --user         user name to use with commands (default: postgres)

Build-only options:
    --with-icu         compile PostgreSQL with ICU support (default: --without-icu)
    --with-openssl     compile PostgreSQL with OpenSSL support (requires libssl headers)
    --configure-opts   extra flags forwarded verbatim to ./configure (quoted string)

Replication options (deploy / cluster):
    --replicate-from   name of an existing sandbox to stream from (turns "deploy" into a standby)
    --slot             physical replication slot name to create on the source
    --sync             register the standby as synchronous on the source
    -N, --nodes        (cluster deploy) number of standbys to provision
    --sync-count       (cluster deploy) how many of the standbys to mark synchronous
    --slot-prefix      (cluster deploy) override slot name prefix (defaults to cluster name)

Run "pg_sandbox help COMMAND" (or "pg_sandbox COMMAND --help") for
detailed help on a single command.
    """)


# Per-command help text. Keep each entry self-contained so users can read it
# without cross-referencing other sections.
_COMMAND_HELP = {
    "build": """Usage:
    pg_sandbox build [OPTIONS] VERSION

Downloads, configures, builds and installs the PostgreSQL major.minor
version supplied as the only positional argument (e.g. "18.3"). Source
is fetched from https://ftp.postgresql.org and the resulting binaries
are installed under PGS_BIN_DIR/<version>/ (defaults to
/opt/postgresql/<version>/).

Subprocess stdout/stderr for every step (configure, make, make install,
contrib make, contrib make install) are persisted under
PGS_BUILD_DIR/logs/<version>/ for later troubleshooting.

Options:
    --with-icu         compile with ICU support (default: --without-icu)
    --with-openssl     compile with OpenSSL support (requires libssl headers)
    --configure-opts="..."
                       extra flags forwarded verbatim to ./configure,
                       e.g. --configure-opts="--enable-tap-tests --with-llvm"

Environment:
    PGS_BUILD_DEBUG=1  add --enable-cassert --enable-debug and debug CFLAGS
    PGS_BIN_DIR        override default install prefix root (/opt/postgresql/)
    PGS_BUILD_DIR      override default build scratch dir (/tmp/postgresql-sandbox-build/)

Example:
    pg_sandbox build --with-openssl --configure-opts="--enable-tap-tests" 18.3
""",

    "deploy": """Usage:
    pg_sandbox deploy -b BIN_DIR -s SANDBOX_DIR [OPTIONS]
    pg_sandbox deploy -b BIN_DIR -s SANDBOX_DIR --replicate-from SRC_SANDBOX [--slot NAME] [--sync]

Initializes a fresh PostgreSQL data directory inside SANDBOX_DIR using
the binaries under BIN_DIR, writes the per-sandbox env file
(pg_sandbox.env), creates the convenience scripts (./start, ./stop,
./use, ...) and starts the server.

If --port is not provided and the default port is busy, deploy will
auto-fallback to the next free port in [default+1, default+100].

When --replicate-from is given, deploy creates a streaming standby of
the named existing sandbox via pg_basebackup -R. The source sandbox is
prepared on demand: wal_level, max_wal_senders and max_replication_slots
are raised if needed (restarting the source if wal_level had to change),
a 'replicator' role is created if missing, and a localhost replication
entry is added to the source's pg_hba.conf. Cascading is supported by
pointing --replicate-from at another standby.

Options:
    -b, --bin           PostgreSQL binary directory (required)
    -s, --sandbox-dir   sandbox directory to create (required, must not exist)
    -D, --datadir       data directory inside the sandbox (default: data)
    -U, --user          PostgreSQL superuser to create (default: postgres)
    -h, --host          host to bind to (default: 127.0.0.1)
    -p, --port          TCP port (default: 65432, auto-fallback if not set)
    -l, --log           server log file inside sandbox (default: server.log)

Replication options:
    --replicate-from SRC_SANDBOX
                        existing sandbox dir to clone via pg_basebackup
    --slot NAME         create + use this physical replication slot on source
    --sync              register this standby as a synchronous standby on
                        the source (appended to synchronous_standby_names)

Examples:
    pg_sandbox deploy -b /opt/postgresql/18.3 -s sbox_18
    pg_sandbox deploy -b /opt/postgresql/18.3 -s sbox_18_s1 \\
        --replicate-from sbox_18 --slot sbox_18_s1_slot
    pg_sandbox deploy -b /opt/postgresql/18.3 -s sbox_18_s2 \\
        --replicate-from sbox_18 --slot sbox_18_s2_slot --sync
""",

    "cluster": """Usage:
    pg_sandbox cluster deploy  -s CLUSTER -b BIN_DIR -N NODES [OPTIONS]
    pg_sandbox cluster status  -s CLUSTER
    pg_sandbox cluster destroy -s CLUSTER [-f]

Provisions, inspects, or tears down a primary + N-standby cluster as a
single unit. Members and metadata are kept together under one
per-cluster parent directory:

    <PGS_ROOT_DIR>/<CLUSTER>/
        cluster.json        manifest tying the members together
        <CLUSTER>_p/        primary
        <CLUSTER>_s1/       standby 1
        <CLUSTER>_s<N>/     standby N

The manifest file at <PGS_ROOT_DIR>/<CLUSTER>/cluster.json ties the
members together for status/destroy.

cluster deploy:
  1. Creates the per-cluster directory <PGS_ROOT_DIR>/<CLUSTER>/.
  2. Deploys the primary <CLUSTER>_p inside it (port = -p value,
     default 65432, with auto-fallback to the next free port).
  3. Deploys each standby <CLUSTER>_s<i> via pg_basebackup -R inside
     the same parent directory, allocating ports near the primary's
     port and creating physical replication slots named
     "<slot-prefix>_s<i>" (slot prefix defaults to CLUSTER).
  4. The first --sync-count standbys are registered as synchronous on
     the primary.
  5. Writes the manifest at <CLUSTER>/cluster.json.

cluster status:
  Prints connection + replication info for every member (uses
  pg_stat_replication on the primary, pg_stat_wal_receiver +
  pg_is_in_recovery() on each standby).

cluster destroy:
  Stops + removes all standbys first (best-effort dropping their slots
  on the primary while it is still running), then the primary, then
  the per-cluster directory (which also takes the manifest with it).
  Honors -f.

Options:
    -s, --sandbox-dir   cluster name (required; used as base for member dirs)
    -b, --bin           PostgreSQL binary directory (required for deploy)
    -N, --nodes         number of standbys to provision (deploy, required >= 1)
    -p, --port          base port for the primary (default 65432, auto-fallback)
    --sync-count K      first K standbys are made synchronous (default 0)
    --slot-prefix PFX   slot name prefix (default: CLUSTER)
    -f, --force         (destroy) skip the confirmation prompt

Examples:
    pg_sandbox cluster deploy  -s rep -b /opt/postgresql/18.3 -N 2 --sync-count 1
    pg_sandbox cluster status  -s rep
    pg_sandbox cluster destroy -s rep -f
""",

    "promote": """Usage:
    pg_sandbox promote -s STANDBY_SANDBOX

Promotes the standby instance backing STANDBOX_SANDBOX to a standalone
primary. Internally runs 'pg_ctl promote -D <datadir>' and waits up to
30 seconds for pg_is_in_recovery() to become false. On success, the
sandbox env file is updated so PGS_ROLE=primary and the standby-only
fields (PGS_REPLICATE_FROM, PGS_SLOT_NAME) are cleared.

Errors out cleanly if the target sandbox is not currently a standby.

Options:
    -s, --sandbox-dir   standby sandbox to promote (required)

Example:
    pg_sandbox promote -s sbox_18_s1
""",

    "destroy": """Usage:
    pg_sandbox destroy -s SANDBOX_DIR [-f]

Stops the PostgreSQL instance backing SANDBOX_DIR (if running) and then
removes the sandbox directory and everything inside it. Without -f the
removal is interactive (asks for confirmation).

Options:
    -s, --sandbox-dir   sandbox directory to destroy (required)
    -f, --force         do not ask for confirmation; remove immediately

Example:
    pg_sandbox destroy -s sbox_18 -f
""",

    "global_status": """Usage:
    pg_sandbox global_status

Lists every sandbox directory under PGS_ROOT_DIR (~/postgresql-sandboxes/),
grouping members of any cluster manifest under that cluster's header and
listing standalone sandboxes separately. Each sandbox is reported as a
single line: state (running/stopped/unknown/missing), name, role, host:port.

No SQL is issued. Only `pg_ctl status` is consulted, so the command is
fast and works against sandboxes whose binaries have moved or whose data
directories were partially deleted (those show up as `unknown`). Members
listed in a cluster manifest but no longer present on disk show up as
`missing`.

Standby rows include a trailing parenthetical with their replication
source and slot (if persisted in the env file).

Options:
    (none)

Example:
    pg_sandbox global_status
""",

    "report": """Usage:
    pg_sandbox report [OPTIONS] OUT_FILE

Spins up a temporary sandbox, loads the pg_gather schema and the supplied
OUT_FILE (an out.txt produced by pg_gather), generates the HTML report
and tears the sandbox down. The resulting report is written next to
OUT_FILE as <basename>.GatherReport.html.

If -b is not provided, the latest binary directory under PGS_BIN_DIR is
auto-selected. If -s is not provided, a temporary sandbox directory
named "pg_gather_temp" is used.

Options:
    -b, --bin           PostgreSQL binary directory (default: latest in PGS_BIN_DIR)
    -s, --sandbox-dir   temporary sandbox directory name (default: pg_gather_temp)
    -f, --force         download missing gather_schema.sql / gather_report.sql
                        without prompting

Environment:
    PGS_PG_GATHER_DIR   directory containing gather_schema.sql / gather_report.sql
                        (default: ~/src/support-snippets/postgresql/pg_gather/)

Example:
    pg_sandbox report -f /path/to/out.txt
""",

    "restart": """Usage:
    pg_sandbox restart -s SANDBOX_DIR

Stops (if running) and then starts the PostgreSQL instance backing
SANDBOX_DIR. Equivalent to running "stop" followed by "start".

Options:
    -s, --sandbox-dir   sandbox directory (required)

Example:
    pg_sandbox restart -s sbox_18
""",

    "run": """Usage:
    pg_sandbox run -s SANDBOX_DIR [-n] BINARY [ARGS...]

Runs an arbitrary PostgreSQL client binary (e.g. pg_dump, pgbench, psql,
pg_basebackup) located under the sandbox's --bin directory. Unless
-n / --no-dsn is supplied, the host/port/user from the sandbox env file
are prepended automatically as -h/-p/-U.

Options:
    -s, --sandbox-dir   sandbox directory (required)
    -n, --no-dsn        do not auto-inject -h/-p/-U; pass ARGS verbatim

Examples:
    pg_sandbox run -s sbox_18 pgbench -i -s 10 postgres
    pg_sandbox run -s sbox_18 -n pg_config --version
""",

    "setenv": """Usage:
    pg_sandbox setenv -s SANDBOX_DIR [OPTIONS]

Re-writes the per-sandbox environment file (pg_sandbox.env) using the
current option values, persisting them for subsequent commands run
against this sandbox.

Useful for changing the default user, dbname, host or port that
follow-up "use" / "run" invocations will use.

Options:
    -s, --sandbox-dir   sandbox directory (required)
    -b, --bin           PostgreSQL binary directory
    -d, --dbname        database name
    -D, --datadir       data directory
    -h, --host          host
    -p, --port          port
    -U, --user          user
    -l, --log           server log file

Example:
    pg_sandbox setenv -s sbox_18 -d mydb -U alice
""",

    "start": """Usage:
    pg_sandbox start -s SANDBOX_DIR

Starts the PostgreSQL instance backing SANDBOX_DIR using the values
persisted in its pg_sandbox.env file. Errors out gracefully if the
instance is already running.

Options:
    -s, --sandbox-dir   sandbox directory (required)

Example:
    pg_sandbox start -s sbox_18
""",

    "status": """Usage:
    pg_sandbox status -s SANDBOX_DIR

Reports whether the PostgreSQL instance backing SANDBOX_DIR is running,
including its PID and command-line arguments. This is a thin wrapper
around 'pg_ctl status -D <datadir>'.

When the instance is running, also prints replication info:
  - role=primary: pg_stat_replication for every connected standby
                  (application_name, state, sync_state, *_lag)
  - role=standby: pg_is_in_recovery() and pg_stat_wal_receiver
                  (status, sender_host, sender_port, slot, latest_end_lsn)

Options:
    -s, --sandbox-dir   sandbox directory (required)

Example:
    pg_sandbox status -s sbox_18
""",

    "stop": """Usage:
    pg_sandbox stop -s SANDBOX_DIR

Stops the PostgreSQL instance backing SANDBOX_DIR using fast shutdown
mode (pg_ctl stop -mf). Reports cleanly if the instance is already
stopped.

Options:
    -s, --sandbox-dir   sandbox directory (required)

Example:
    pg_sandbox stop -s sbox_18
""",

    "use": """Usage:
    pg_sandbox use -s SANDBOX_DIR [PSQL_ARGS...]

Launches the psql client connected to the sandbox using the host, port,
user and dbname persisted in its pg_sandbox.env file. Any additional
arguments are forwarded verbatim to psql.

Options:
    -s, --sandbox-dir   sandbox directory (required)
    -d, --dbname        override target database name
    -U, --user          override connecting user

Examples:
    pg_sandbox use -s sbox_18
    pg_sandbox use -s sbox_18 -X -f my_script.sql
    pg_sandbox use -s sbox_18 -c "SELECT version()"
""",
}


def print_help(pgs_command):
    text = _COMMAND_HELP.get(pgs_command)
    if text is None:
        print_general_help()
    else:
        print(text)
