## PostgreSQL Sandbox Quick Demo Commands.

Repo at: https://github.com/guriandoro/postgresql_sandbox

Check help outputs
```
pg_sandbox help
```

Build a new postgres version we don't have. Since PostgreSQL doesn't offer tarball releases, we have to compile it on our own. We are also compiling the contrib packages, so we have the typical extensions (like pg_stat_statements) available to use
```
pg_sandbox build 15.3
```

Deploy our first sandbox
```
pg_sandbox deploy -b /opt/postgresql/15.3/ -s pg-15.3
```

Change dir to postgres sandboxes home (if it wasn't already created, it will prompt to create)
```
cd ~/postgresql-sandboxes/
ls -l
```

Try to create another sandbox with same command (it will generate a port error)
```
pg_sandbox deploy -b /opt/postgresql/15.3/ -s pg-15.3
```

Override default port (but we are still using the same directory, so it will also error out)
```
pg_sandbox deploy -b /opt/postgresql/15.3/ -s pg-15.3 -p 23444
```

Change the sandbox directory used (this command will succeed)
```
pg_sandbox deploy -b /opt/postgresql/15.3/ -s another-pg-15.3 -p 23444
```

Use the first sandbox deployed
```
cd ~/postgresql-sandboxes/pg-15.3
```

Check all the scripts that are created (and investigate what they do).
They all have their pg_sandbox command counterpart
```
ls -l
```

Connect to psql
```
./use
```

This is the same as using the following command
```
pg_sandbox use
```

Connect to psql from another directory (we can use the -s argument to tell which sandbox we want to work with)
```
cd
pg_sandbox use -s pg-15.3
```

Go back to our sandbox dir
```
cd ~/postgresql-sandboxes/pg-15.3
ls -l
```

Stop server
```
./stop
```

Start server
```
./start
```

Restart server
```
./restart
```

Check server status
```
./status
```

Insert some data into the instance
```
curl -LO https://raw.githubusercontent.com/guriandoro/postgresql_sandbox/master/insert_simple_test_data.sql
./use < insert_simple_test_data.sql
```

Check which binaries we have in the bin dir, to use with the ./run command
```
s -l /opt/postgresql/15.3/bin/
```

Use pg_dump to create a new dump of the postgres database
```
./run pg_dump postgres > pg_dump_postgres.sql
ls -lh pg_dump_postgres.sql
```
Dump with extra arguments: schema only
```
./run pg_dump -s postgres > pg_dump_s_postgres.sql
```

Dump with extra arguments: data only
```
./run pg_dump -a postgres > pg_dump_a_postgres.sql
ls -l pg_dump*
```
Use the createuser script
```
./run createuser --interactive pmm
```

Destroy sandbox
```
pg_sandbox destroy
```

Destroy sandbox with force flag (this will answer "yes" to all questions)
```
pg_sandbox destroy -f
```

Generate pg_gather report from out.txt file
```
pg_sandbox report out.tx
```

Reply "yes" to all questions when generating the pg_gather report
```
pg_sandbox report -f out.txt
```
