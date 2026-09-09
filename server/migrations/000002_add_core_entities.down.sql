-- 000002_add_core_entities(down): 按依赖倒序删表
DROP TABLE IF EXISTS cases;
DROP TABLE IF EXISTS datasets;
DROP TABLE IF EXISTS documents;
DROP TABLE IF EXISTS corpora;
DROP TABLE IF EXISTS projects;
DROP TABLE IF EXISTS users;