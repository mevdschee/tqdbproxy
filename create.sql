-- cat create_mysql.sql | sudo mysql
--
DROP USER IF EXISTS 'tqdbproxy'@'localhost';
DROP DATABASE IF EXISTS `tqdbproxy`;
--
CREATE DATABASE `tqdbproxy` CHARACTER SET utf8mb4 COLLATE utf8mb4_general_ci;
CREATE USER 'tqdbproxy'@'localhost' IDENTIFIED BY 'tqdbproxy';
GRANT ALL PRIVILEGES ON `tqdbproxy`.* TO 'tqdbproxy'@'localhost' WITH GRANT OPTION;
FLUSH PRIVILEGES;
