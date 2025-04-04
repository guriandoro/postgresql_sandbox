# PostgreSQL Sandbox Build Command

## Usage
```
pg_sandbox build <version>
```

## Arguments
- `<version>`: Required. The major.minor PostgreSQL version number (e.g., "15.3", "16.0")

## Description
The `build` command compiles PostgreSQL from source for the specified version. It:
- Downloads the PostgreSQL source code for the specified version
- Configures and compiles PostgreSQL
- Installs it to `/opt/postgresql/<version>/`
- Also compiles and installs contrib packages (extensions)

## Example
```
pg_sandbox build 15.3
```

## Technical Details
The build command uses a standard build configuration with:
- `--prefix="/tmp/opt/postgresql/${VERSION}/"` for installation
- `-j8` for parallel compilation
- Includes contrib packages compilation

The command creates temporary build directories in `/tmp/pg_src/` and `/tmp/opt/postgresql/` during the build process.

**Note**: This command requires you to have the necessary build dependencies installed on your system to compile PostgreSQL from source.

## Output Files
The build process generates these output files:
- `/tmp/pg_sandbox_build_configure.out` - Configure output
- `/tmp/pg_sandbox_build_make.out` - Make output
- `/tmp/pg_sandbox_build_install.out` - Install output
- `/tmp/pg_sandbox_build_contrib_make.out` - Contrib make output
- `/tmp/pg_sandbox_build_contrib_install.out` - Contrib install output 

# PostgreSQL Sandbox Deploy Command

## Usage
```
pg_sandbox deploy [OPTIONS]
```

## Description
The `deploy` command initializes a new PostgreSQL instance and starts it. It:
- Creates a sandbox directory
- Initializes a PostgreSQL database in the data directory
- Writes environment variables to the pg_sandbox.env file
- Creates handy scripts in the sandbox directory
- Starts the PostgreSQL server

## Options
The deploy command accepts the standard pg_sandbox options:
- `-b, --bin` - PostgreSQL binary directory
- `-s, --sandbox-dir` - Directory to use for the sandbox
- `-p, --port` - Port to use (default: 65432)
- `-d, --dbname` - Database name (default: postgres)
- `-D, --datadir` - Data directory within the sandbox directory (default: data)
- `-U, --user` - User name (default: postgres)

## Example
```
pg_sandbox deploy -b /opt/postgresql/15.3/bin -s pg-15.3
```

## Notes
- The command will check if the port is already in use and abort if it is
- If the sandbox directory already exists, the command will abort

# PostgreSQL Sandbox Destroy Command

## Usage
```
pg_sandbox destroy [OPTIONS]
```

## Description
The `destroy` command stops the PostgreSQL instance and removes the sandbox directory. It:
- Stops the PostgreSQL server if it's running
- Asks for confirmation before removing the sandbox directory
- Removes the sandbox directory and all its contents

## Options
- `-f, --force` - Assume "yes" for the confirmation prompt

## Example
```
pg_sandbox destroy -f
```

## Notes
- Without the `-f` option, the command will prompt for confirmation before removing the directory
- If the server is already stopped, the command will simply proceed with the directory removal

# PostgreSQL Sandbox Report Command

## Usage
```
pg_sandbox report <out.txt>
```

## Arguments
- `<out.txt>`: Required. The pg_gather output file to process

## Description
The `report` command generates a pg_gather HTML report from an out.txt file. It:
- Creates a temporary sandbox with a PostgreSQL instance
- Imports the data from the out.txt file
- Generates an HTML report using pg_gather scripts
- Destroys the temporary sandbox

## Options
- `-f, --force` - Assume "yes" for confirmation prompts and downloads

## Example
```
pg_sandbox report out.txt
pg_sandbox report -f out.txt
```

## Notes
- This command requires the pg_gather project to be accessible in ~/src/ or will download it
- The output file will be named `<out.txt>_GatherReport.html` in the same directory as the input file

# PostgreSQL Sandbox Restart Command

## Usage
```
pg_sandbox restart [OPTIONS]
```

## Description
The `restart` command restarts the PostgreSQL instance. It:
- Stops the PostgreSQL server if it's running
- Starts the PostgreSQL server again

## Example
```
pg_sandbox restart
```

## Notes
- The command performs a full stop and start, not just a reload
- If the server isn't running, it will just start it

# PostgreSQL Sandbox Run Command

## Usage
```
pg_sandbox run <binary> [ARGUMENTS]
```

## Arguments
- `<binary>`: Required. The PostgreSQL binary to run (e.g., pg_dump, createdb)
- `[ARGUMENTS]`: Optional. Arguments to pass to the binary

## Description
The `run` command executes a specified PostgreSQL binary with the provided arguments. By default, it adds connection parameters (host, port, user) to the command.

## Options
- `-n, --no-dsn` - Don't add connection parameters (host, port, user) to the binary

## Examples
```
pg_sandbox run pg_dump postgres > dump.sql
pg_sandbox run createuser --interactive testuser
pg_sandbox -n run pg_config
```

## Notes
- This command is useful for running PostgreSQL utilities and administration commands
- The binary must be available in the bin directory specified with `-b`

# PostgreSQL Sandbox Setenv Command

## Usage
```
pg_sandbox setenv [OPTIONS]
```

## Description
The `setenv` command writes environment variables to the pg_sandbox.env file. This allows you to run other commands without having to specify all options every time.

## Example
```
pg_sandbox -b /opt/postgresql/15.3/bin -s pg-15.3 -p 23444 setenv
```

## Notes
- After running this command, you can run other commands like `start`, `stop`, etc. without having to specify options
- The environment variables are read from the pg_sandbox.env file in the sandbox directory

# PostgreSQL Sandbox Start Command

## Usage
```
pg_sandbox start [OPTIONS]
```

## Description
The `start` command starts the PostgreSQL instance.

## Example
```
pg_sandbox start
```

## Notes
- If the server is already running, the command will display a message and exit
- The command uses pg_ctl to start the server with the configured port

# PostgreSQL Sandbox Stop Command

## Usage
```
pg_sandbox stop [OPTIONS]
```

## Description
The `stop` command stops the PostgreSQL instance.

## Example
```
pg_sandbox stop
```

## Notes
- If the server is already stopped, the command will display a message and exit
- The command uses pg_ctl with "fast" shutdown mode

# PostgreSQL Sandbox Use Command

## Usage
```
pg_sandbox use [PSQL_OPTIONS]
```

## Arguments
- `[PSQL_OPTIONS]`: Optional. Any options to pass directly to the psql client

## Description
The `use` command runs the psql client connected to the PostgreSQL instance. All arguments after "use" are sent directly to psql.

## Examples
```
pg_sandbox use
pg_sandbox use -c "SELECT version();"
pg_sandbox use -f script.sql
```

## Notes
- The command runs psql with the connection parameters (host, port, user, database) configured for the sandbox
- This is the primary way to interact with the database 