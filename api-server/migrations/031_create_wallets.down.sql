DROP TRIGGER  IF EXISTS trg_create_wallet ON users;
DROP FUNCTION IF EXISTS create_wallet_for_new_user();
DROP TABLE    IF EXISTS wallet_transactions;
DROP TABLE    IF EXISTS wallets;
