def print_help():
    print("""Usage:
    pg_sandbox COMMAND [OPTIONS] SUBCOMMAND [POSITIONAL_ARGUMENTS]

Commands:
    build              compile PostgreSQL for the given major.minor version (as first argument after "build")
    create             initialize the PostgreSQL instance, and start it
    destroy            stop the PostgreSQL instance, and delete all directories
    help               print this message and exit
    report             generate pg_gather report from out.txt output file.
    restart            restart the PostgreSQL instance
    run                runs the binary specified by the subcommand with the positional arguments
    setenv             write variables to the environment variables file
    start              start the PostgreSQL instance
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
    """)
