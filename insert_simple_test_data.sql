-- varchar   -- md5(x::text)
-- timestamp -- now() + random() * (timestamp '2010-01-01 00:00:00' -  now())
-- int       -- (random() * 99999)::int

\set joinit1_tuple_count 1000000
\set joinit2_tuple_count 200000
\set joinit3_tuple_count 100000

CREATE SCHEMA test;

DROP TABLE IF EXISTS test.joinit3;
DROP TABLE IF EXISTS test.joinit2;
DROP TABLE IF EXISTS test.joinit1;

CREATE TABLE test.joinit1 (
  i int NOT NULL,
  s varchar DEFAULT NULL,
  t timestamp NOT NULL,
  g int NOT NULL,
  PRIMARY KEY (i)
);
CREATE INDEX idx_joinit1_1 ON test.joinit1 (g);
CREATE INDEX idx_joinit1_2 ON test.joinit1 (g,i);

INSERT INTO test.joinit1 
SELECT x, md5(x::text), now() + random() * (timestamp '2010-01-01 00:00:00' -  now()), (random() * 99999)::int
FROM generate_series(0, (:joinit1_tuple_count-1)) x;

CREATE TABLE test.joinit2 (
  i int NOT NULL,
  j int NULL,
  k int NULL,
  s varchar DEFAULT NULL,
  g int NOT NULL REFERENCES test.joinit1 (i),
  PRIMARY KEY (i)
);

INSERT INTO test.joinit2
SELECT x, (random() * 100)::int, (random() * 10000)::int, md5(x::text), (random() * (:joinit1_tuple_count-1))::int 
FROM generate_series(0, (:joinit2_tuple_count-1)) x;

CREATE TABLE test.joinit3 (
  i int NOT NULL,
  s varchar,
  t varchar,
  u varchar,
  g int NOT NULL REFERENCES test.joinit2 (i),
  h int NOT NULL,
  PRIMARY KEY (i)
);
CREATE INDEX idx_joinit3_1 ON test.joinit3 (g, h);

INSERT INTO test.joinit3
SELECT x, md5(x::text), md5(x::text), md5(x::text), (random() * (:joinit2_tuple_count-1))::int, (random() * 10)::int
FROM generate_series(0, (:joinit3_tuple_count-1)) x;

SELECT COUNT(*) FROM test.joinit1;
SELECT COUNT(*) FROM test.joinit2;
SELECT COUNT(*) FROM test.joinit3;
