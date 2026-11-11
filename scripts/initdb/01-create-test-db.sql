-- Integration tests use their own database, so they never wipe the data
-- a reviewer is exploring in "scheduler".
CREATE DATABASE scheduler_test;
