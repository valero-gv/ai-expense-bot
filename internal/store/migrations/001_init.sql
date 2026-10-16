CREATE TABLE users (
    id BIGINT PRIMARY KEY CHECK (id > 0),
    label TEXT NOT NULL DEFAULT ''
);

CREATE TABLE expenses (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES users(id),
    amount NUMERIC(11, 2) NOT NULL CHECK (amount > 0 AND amount <= 999999999.99),
    category TEXT NOT NULL CHECK (category IN ('еда', 'транспорт', 'жильё', 'развлечения', 'здоровье', 'покупки', 'другое')),
    spent_at DATE NOT NULL,
    comment TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);

CREATE INDEX expenses_user_date_idx ON expenses(user_id, spent_at DESC, id DESC);
CREATE INDEX expenses_user_created_idx ON expenses(user_id, created_at DESC, id DESC);
CREATE INDEX expenses_date_idx ON expenses(spent_at DESC, id DESC);

CREATE TABLE conversation_history (
    user_id BIGINT PRIMARY KEY REFERENCES users(id),
    payload BYTEA NOT NULL
);

CREATE TABLE processed_updates (
    user_id BIGINT NOT NULL REFERENCES users(id),
    update_id BIGINT NOT NULL CHECK (update_id >= 0),
    reply TEXT NOT NULL,
    processed_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (user_id, update_id)
);
