#!/usr/bin/env bash
# pg_sandbox — live demo cheat-sheet
#
# This is a PASTE SOURCE, not a run-it-top-to-bottom script. Each block maps to
# one slide; paste the block when you reach that slide. Run the whole demo in ONE
# terminal session so `cd`/`export` persist.
#
# Every command below was executed end-to-end against /opt/postgresql on this
# machine (2026-06-23) and verified working.

# ── PREP (run once, before the talk) ─────────────────────────────────────────
# latest installed; NOT 18.4 (18.4 is not installed)
export PGS_BIN_DIR=/opt/postgresql/18.3
# pg_gather scripts location, used by slide 6
export GATHER=/Users/agustin/src/support-snippets/postgresql/pg_gather
mkdir -p ~/postgresql-sandboxes
# bare-name `deploy` lands in CWD -> keep CWD here
cd ~/postgresql-sandboxes
# v1.0.1 Go binary (verified on PATH)
pg_sandbox --version
# confirm a clean slate
pg_sandbox global_status


# ── SLIDE 2: single instance lifecycle ───────────────────────────────────────
pg_sandbox deploy -s pg18
pg_sandbox status -s pg18
pg_sandbox use -s pg18 -- -c "SELECT version();"
# NO positional db: -d is auto-injected
pg_sandbox run -s pg18 -- pgbench -i
# ~6k tps locally — nice live number
pg_sandbox run -s pg18 -- pgbench -T 3
# KEY / VALUE / SOURCE provenance table
pg_sandbox config show -s pg18
pg_sandbox destroy -s pg18 --force


# ── SLIDE 3: streaming replication ───────────────────────────────────────────
pg_sandbox deploy -s primary
pg_sandbox deploy -s standby1 --replicate-from primary --slot primary_standby1_slot
# let walreceiver attach before status
sleep 2
# replicas[0]=app=walreceiver state=streaming
pg_sandbox status -s primary
pg_sandbox use -s primary  -- -c "CREATE TABLE t(id int); INSERT INTO t VALUES (1);"
# returns the row — replicated
pg_sandbox use -s standby1 -- -c "SELECT * FROM t;"
# standby1 role -> primary
pg_sandbox promote -s standby1
pg_sandbox destroy -s standby1 --force
pg_sandbox destroy -s primary  --force


# ── SLIDE 4: one-shot cluster ─────────────────────────────────────────────────
# rep_p + rep_s1 + rep_s2, ports 65432/3/4
pg_sandbox cluster deploy -s rep -N 2
sleep 2
# primary shows BOTH replicas streaming
pg_sandbox cluster status -s rep
# grouped table: cluster rep, 3 members
pg_sandbox global_status
pg_sandbox cluster destroy -s rep --force


# ── SLIDE 5: logical replication across major versions (PG 17 -> 18) ──────────
# -b pins each sandbox to a different major; logical repl streams 17.4 -> 18.3.
# publisher on PG 17
pg_sandbox deploy  -s pub -b /opt/postgresql/17.4
pg_sandbox use     -s pub -- -c "CREATE TABLE t(id int primary key); INSERT INTO t VALUES (1);"
# auto-raises wal_level=logical + restarts
pg_sandbox publish -s pub --pub-name my_pub --all-tables
# subscriber on PG 18
pg_sandbox deploy    -s sub -b /opt/postgresql/18.3
# NOTE: --from (not --subscribe-to)
pg_sandbox subscribe -s sub --from pub --pub-name my_pub --copy-schema
sleep 2
# returns the row — replicated logically
pg_sandbox use -s sub -- -c "SELECT * FROM t;"
# ongoing changes stream too
pg_sandbox use -s pub -- -c "INSERT INTO t VALUES (2);"
sleep 1
# -> 2
pg_sandbox use -s sub -- -c "SELECT count(*) FROM t;"
pg_sandbox destroy -s sub --force
pg_sandbox destroy -s pub --force


# ── SLIDE 6: pg_gather report ─────────────────────────────────────────────────
# Capture a real out.txt from a live sandbox, then render the HTML report.
pg_sandbox deploy -s pg18
# give the report something to show
pg_sandbox run -s pg18 -- pgbench -i
pg_sandbox use -s pg18 -- -X -f "$GATHER/gather.sql" > ~/postgresql-sandboxes/out.txt
# NOTE: the CWD is never searched for gather scripts (security hardening);
# point at them with --pg-gather-dir (or export PGS_PG_GATHER_DIR). Spins up a
# throwaway _report_* sandbox, ingests out.txt, renders ~190 KB HTML, self-destructs.

# We can omit --output and it will write in the same directory as --input
pg_sandbox report --pg-gather-dir "$GATHER" \
                  --input ~/postgresql-sandboxes/out.txt \
                  --output ~/postgresql-sandboxes/gather.html
# macOS — opens the report in the browser
open ~/postgresql-sandboxes/gather.html
pg_sandbox destroy -s pg18 --force


# ── TEARDOWN (safety net) ─────────────────────────────────────────────────────
rm -f ~/postgresql-sandboxes/out.txt ~/postgresql-sandboxes/gather.html
# expect "(no sandboxes or clusters found)"
pg_sandbox global_status
