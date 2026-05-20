def print_general_help():
    print("""Usage:
    pg_sandbox COMMAND [OPTIONS] SUBCOMMAND [POSITIONAL_ARGUMENTS]

Commands:
    build              compile PostgreSQL for the given major.minor version (as first argument after "build")
    cleanup-install-versions  remove install tree(s) under PGS_BIN_DIR, or prune installs not referenced by any sandbox
    cluster            provision/inspect/destroy a primary + N standby (or subscriber) cluster ("cluster deploy|status|destroy")
    deploy             initialize the PostgreSQL instance, and start it (or attach as a standby with --replicate-from, or as a subscriber with --subscribe-to)
    destroy            stop the PostgreSQL instance, and delete all directories
    global_status      list every sandbox under PGS_ROOT_DIR with its running/stopped state and role
    help               print this message and exit
    promote            promote a standby sandbox to a standalone primary
    publish            create a logical replication publication on an existing sandbox
    report             generate pg_gather report from out.txt output file.
    restart            restart the PostgreSQL instance
    run                runs the binary specified by the subcommand with the positional arguments
    setenv             write variables to the environment variables file
    start              start the PostgreSQL instance
    status             show the PostgreSQL instance status (incl. replication info when running)
    stop               stop the PostgreSQL instance
    subscribe          create a logical replication subscription on an existing sandbox
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
    --sync             register the standby/subscriber as synchronous on the source
    -N, --nodes        (cluster deploy) number of standbys/subscribers to provision
    --sync-count       (cluster deploy) how many of the standbys/subscribers to mark synchronous
    --slot-prefix      (cluster deploy) override slot/subscription name prefix (defaults to cluster name)

Logical replication options (deploy / publish / subscribe / cluster):
    --subscribe-to     (deploy) name of an existing sandbox to subscribe to (turns "deploy" into a subscriber)
    --from             (subscribe) alias of --subscribe-to for the standalone subcommand
    --pub-name         publication name (used by deploy --subscribe-to, publish, subscribe)
    --sub-name         subscription name (default: <basename(sandbox)>_sub)
    --copy-schema      copy publisher's schema (pg_dump --schema-only | psql) before subscribing
    --no-copy-data     create the subscription with WITH (copy_data = false)
    --all-tables       (publish / cluster --logical) publish FOR ALL TABLES
    --tables           (publish) publish FOR TABLE T1,T2,... (comma-separated)
    --logical          (cluster deploy) build a logical-replication cluster (1 publisher + N subscribers)
    --logical-pub-name (cluster deploy --logical) override the cluster-wide publication name (default: pgs_pub)

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

    "cleanup-install-versions": """Usage:
    pg_sandbox cleanup-install-versions [-f] [NAME ...]
    pg_sandbox cleanup-install-versions [-f]

Removes one or more PostgreSQL install directories from PGS_BIN_DIR
(default /opt/postgresql/). Each NAME must be a single subdirectory
name (not a path), for example '18.3' or '2026.05.05-84a231c'.

Refuses to delete any directory whose real path matches PGS_BIN in a
sandbox's pg_sandbox.env (including cluster members), or that is a
prefix of a referenced install tree.

With no NAME arguments, lists every install under PGS_BIN_DIR that looks
like PostgreSQL (has bin/postgres or bin/pg_ctl) and is not referenced
by any sandbox, then prompts once to delete them all (skipped if none).

Options:
    -f, --force        skip confirmation prompts (still refuses in-use installs)

Environment:
    PGS_BIN_DIR        install location root (default /opt/postgresql/)
    PGS_ROOT_DIR       walked to discover which installs are referenced

Examples:
    pg_sandbox cleanup-install-versions 2026.05.05-84a231c
    pg_sandbox cleanup-install-versions -f 18.2 18.1
    pg_sandbox cleanup-install-versions
""",

    "deploy": """Usage:
    pg_sandbox deploy -b BIN_DIR -s SANDBOX_DIR [OPTIONS]
    pg_sandbox deploy -b BIN_DIR -s SANDBOX_DIR --replicate-from SRC_SANDBOX [--slot NAME] [--sync]
    pg_sandbox deploy -b BIN_DIR -s SANDBOX_DIR --subscribe-to SRC_SANDBOX --pub-name PUB \\
                       [--sub-name SUB] [-d DBNAME] [--copy-schema] [--no-copy-data] [--sync]

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

When --subscribe-to is given, deploy creates a *fresh primary* sandbox
and attaches it to the named source via CREATE SUBSCRIPTION. The source
is bootstrapped for logical replication on demand (wal_level escalated
to 'logical', replication role + pg_hba entry created). The publication
named by --pub-name must already exist on the source (e.g. created via
'pg_sandbox publish'). Logical replication does NOT replicate DDL, so
the subscriber needs a matching schema; pass --copy-schema to bootstrap
it via 'pg_dump --schema-only | psql' before the subscription is
created. --replicate-from and --subscribe-to are mutually exclusive.

Options:
    -b, --bin           PostgreSQL binary directory (required)
    -s, --sandbox-dir   sandbox directory to create (required, must not exist)
    -d, --dbname        database name (default: postgres; for --subscribe-to,
                        the publication and subscription target db)
    -D, --datadir       data directory inside the sandbox (default: data)
    -U, --user          PostgreSQL superuser to create (default: postgres)
    -h, --host          host to bind to (default: 127.0.0.1)
    -p, --port          TCP port (default: 65432, auto-fallback if not set)
    -l, --log           server log file inside sandbox (default: server.log)

Physical replication options:
    --replicate-from SRC_SANDBOX
                        existing sandbox dir to clone via pg_basebackup
    --slot NAME         create + use this physical replication slot on source
    --sync              register this standby as a synchronous standby on
                        the source (appended to synchronous_standby_names)

Logical replication options:
    --subscribe-to SRC_SANDBOX
                        existing sandbox to subscribe to (turns deploy into
                        a fresh primary that subscribes to SRC_SANDBOX)
    --pub-name PUB      publication name on the source (required with
                        --subscribe-to; the publication must already exist)
    --sub-name SUB      subscription name on the new sandbox
                        (default: <basename(SANDBOX_DIR)>_sub)
    --copy-schema       run 'pg_dump -s | psql' from source to subscriber
                        before creating the subscription
    --no-copy-data      create the subscription with WITH (copy_data = false);
                        existing rows on the publisher are NOT initially copied
    --sync              register this subscriber as synchronous on the source
                        (matches via the subscription's application_name)

Examples:
    pg_sandbox deploy -b /opt/postgresql/18.3 -s sbox_18
    pg_sandbox deploy -b /opt/postgresql/18.3 -s sbox_18_s1 \\
        --replicate-from sbox_18 --slot sbox_18_s1_slot
    pg_sandbox deploy -b /opt/postgresql/18.3 -s sbox_18_s2 \\
        --replicate-from sbox_18 --slot sbox_18_s2_slot --sync

    # First, create a publication on the source:
    pg_sandbox publish   -s sbox_18 --pub-name app_pub --all-tables
    # Then deploy a fresh subscriber sandbox attached to it:
    pg_sandbox deploy -b /opt/postgresql/18.3 -s sbox_18_sub \\
        --subscribe-to sbox_18 --pub-name app_pub --copy-schema
""",

    "cluster": """Usage:
    pg_sandbox cluster deploy  -s CLUSTER -b BIN_DIR -N NODES [OPTIONS]
    pg_sandbox cluster deploy  -s CLUSTER -b BIN_DIR -N NODES --logical [LOGICAL_OPTIONS]
    pg_sandbox cluster status  -s CLUSTER
    pg_sandbox cluster destroy -s CLUSTER [-f]

Provisions, inspects, or tears down a primary + N-standby (physical) or
1 publisher + N-subscriber (logical) cluster as a single unit. Members
and metadata are kept together under one per-cluster parent directory:

    <PGS_ROOT_DIR>/<CLUSTER>/
        cluster.json        manifest tying the members together
        <CLUSTER>_p/        primary (publisher in logical mode)
        <CLUSTER>_s1/       standby/subscriber 1
        <CLUSTER>_s<N>/     standby/subscriber N

The manifest file at <PGS_ROOT_DIR>/<CLUSTER>/cluster.json ties the
members together for status/destroy. Manifest gains "mode": "physical"
or "logical" plus mode-specific fields ("slots" vs
"publication"/"subscriptions"/"dbname").

cluster deploy (physical, default):
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

cluster deploy --logical:
  1. Creates the per-cluster directory and deploys the publisher
     <CLUSTER>_p as a primary (wal_level escalated to 'logical').
  2. Creates a single cluster-wide publication on the publisher (name
     from --logical-pub-name, default 'pgs_pub') in db -d (default
     'postgres'). Defaults to FOR ALL TABLES; use --tables T1,T2,...
     to scope it instead.
  3. Deploys each subscriber <CLUSTER>_s<i> as a fresh primary, then
     creates a subscription named "<slot-prefix>_s<i>_sub" pointing
     at the publisher / publication. With --copy-schema, runs
     'pg_dump -s | psql' from publisher to each subscriber before
     creating the subscription so the initial copy_data has table
     definitions to land into.
  4. The first --sync-count subscribers are registered as synchronous
     on the publisher (matched by application_name == sub name).

cluster status:
  Prints connection + replication info for every member (uses
  pg_stat_replication on the primary/publisher, pg_stat_wal_receiver
  + pg_is_in_recovery() on each physical standby, and
  pg_publication / pg_subscription / pg_stat_subscription on every
  member that participates in logical replication).

cluster destroy:
  Stops + removes all standbys/subscribers first, then the primary,
  then the per-cluster directory. In physical mode, slots on the
  primary are best-effort dropped while it is still running. In
  logical mode, subscriptions are best-effort dropped on each
  subscriber first (so the publisher reclaims the logical slot
  cleanly), then the publication is dropped on the publisher.
  Honors -f.

Options:
    -s, --sandbox-dir   cluster name (required; used as base for member dirs)
    -b, --bin           PostgreSQL binary directory (required for deploy)
    -N, --nodes         number of standbys/subscribers to provision (deploy, required >= 1)
    -p, --port          base port for the primary (default 65432, auto-fallback)
    --sync-count K      first K standbys/subscribers are made synchronous (default 0)
    --slot-prefix PFX   slot/subscription name prefix (default: CLUSTER)
    -f, --force         (destroy) skip the confirmation prompt

Logical-mode options (--logical):
    --logical                 build a 1 publisher + N subscribers cluster
    --logical-pub-name NAME   cluster-wide publication name (default: pgs_pub)
    -d, --dbname DB           database holding the publication (default: postgres)
    --all-tables              publish FOR ALL TABLES (default in --logical mode)
    --tables T1,T2,...        publish FOR TABLE T1,T2,... instead of all tables
    --copy-schema             pg_dump -s | psql from publisher to each subscriber

Examples:
    pg_sandbox cluster deploy  -s rep -b /opt/postgresql/18.3 -N 2 --sync-count 1
    pg_sandbox cluster status  -s rep
    pg_sandbox cluster destroy -s rep -f

    pg_sandbox cluster deploy  -s lrep -b /opt/postgresql/18.3 -N 2 --logical \\
        --copy-schema
    pg_sandbox cluster destroy -s lrep -f
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

    "publish": """Usage:
    pg_sandbox publish -s SANDBOX_DIR --pub-name PUB [-d DBNAME] (--all-tables | --tables T1,T2,...)

Creates a logical replication publication named PUB on the running
SANDBOX_DIR. The sandbox is bootstrapped for logical replication on
demand: wal_level escalated to 'logical' (which requires a server
restart), max_wal_senders / max_replication_slots raised if too low,
the 'replicator' role created if missing, and a localhost replication
entry appended to pg_hba.conf.

If a publication with the same name already exists, the call is a
no-op. The publication is created in dbname -d (default: postgres).
Pass --all-tables for FOR ALL TABLES, or --tables T1,T2,... for a
specific list (comma-separated; schema-qualified names like
"public.t1" are allowed).

Once the publication exists, attach a subscriber with either of:
    pg_sandbox subscribe -s SUB_SANDBOX --from PUB_SANDBOX --pub-name PUB ...
    pg_sandbox deploy    -b BIN -s NEW_SANDBOX --subscribe-to PUB_SANDBOX --pub-name PUB ...

Options:
    -s, --sandbox-dir   sandbox to publish from (required, must be running)
    --pub-name NAME     publication name (required)
    -d, --dbname DB     database holding the publication (default: postgres)
    --all-tables        publish FOR ALL TABLES
    --tables T1,T2,...  publish FOR TABLE T1,T2,... (mutually exclusive with --all-tables)

Examples:
    pg_sandbox publish -s sbox_18 --pub-name app_pub --all-tables
    pg_sandbox publish -s sbox_18 --pub-name orders_pub --tables public.orders,public.line_items
""",

    "subscribe": """Usage:
    pg_sandbox subscribe -s TARGET_SANDBOX --from SRC_SANDBOX --pub-name PUB \\
                         [--sub-name SUB] [-d DBNAME] [--copy-schema] [--no-copy-data] [--sync]

Creates a logical replication subscription on the running TARGET_SANDBOX
attached to a publication on SRC_SANDBOX. Both sandboxes must be running.
The source publisher is bootstrapped for logical replication on demand
(same wal_level / role / pg_hba prep as 'publish'). The target stays a
primary -- a logical subscriber is just a primary that receives changes
from the publisher.

Logical replication does NOT replicate DDL, so the subscriber needs a
matching schema for the replicated tables. Pass --copy-schema to
bootstrap it via 'pg_dump --schema-only | psql' before CREATE
SUBSCRIPTION runs (so the initial copy_data has table definitions to
land into).

Options:
    -s, --sandbox-dir   target sandbox where the subscription will be created (required)
    --from SRC_SANDBOX  source sandbox holding the publication (required)
    --subscribe-to SRC  alias for --from
    --pub-name PUB      publication name on SRC_SANDBOX (required)
    --sub-name SUB      subscription name on the target (default: <basename(target)>_sub)
    -d, --dbname DB     database (publisher + subscriber must use the same; default: postgres)
    --copy-schema       run 'pg_dump -s | psql' from source to target before subscribing
    --no-copy-data      create the subscription WITH (copy_data = false)
    --sync              register this subscriber as synchronous on the source

Examples:
    pg_sandbox subscribe -s sbox_18_sub --from sbox_18 --pub-name app_pub --copy-schema
    pg_sandbox subscribe -s sbox_18_sub --from sbox_18 --pub-name app_pub \\
        --sub-name custom_sub --no-copy-data
""",

    "destroy": """Usage:
    pg_sandbox destroy -s SANDBOX_DIR [-f]

Stops the PostgreSQL instance backing SANDBOX_DIR (if running) and then
removes the sandbox directory and everything inside it. Without -f the
removal is interactive (asks for confirmation).

If the sandbox env file records a logical-replication subscription
(PGS_SUBSCRIPTION_NAME), destroy best-effort runs DROP SUBSCRIPTION
*before* stopping the instance so the publisher reclaims the
corresponding logical slot cleanly (logical slots pin WAL until
dropped). Failures of that pre-drop are non-fatal.

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
  - any role:     pg_publication and pg_subscription / pg_stat_subscription
                  (so logical publishers/subscribers are surfaced uniformly)

Options:
    -s, --sandbox-dir   sandbox directory (required)

Example:
    pg_sandbox status -s sbox_18
""",

    "stop": """Usage:
    pg_sandbox stop -s SANDBOX_DIR

Stops sandbox instances using fast shutdown mode (pg_ctl stop -mf).

If SANDBOX_DIR contains pg_sandbox.env, stop targets that single
sandbox (backward-compatible behavior).

Otherwise SANDBOX_DIR is treated as a parent directory and pg_sandbox
recursively scans child directories; every directory containing
pg_sandbox.env is treated as a sandbox and stopped independently.
Failures on one sandbox do not stop iteration; a non-zero exit is
returned at the end if any sandbox failed.

Options:
    -s, --sandbox-dir   sandbox directory or parent directory (required)

Example:
    pg_sandbox stop -s sbox_18
    pg_sandbox stop -s rep
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
