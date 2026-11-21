def bad(conn, merchant_id):
    # ruleid: fin-006-sql-string-building
    conn.execute(f"SELECT * FROM t WHERE m = '{merchant_id}'")
    # ruleid: fin-006-sql-string-building
    conn.execute("SELECT * FROM t WHERE m = '%s'" % merchant_id)
    # ruleid: fin-006-sql-string-building
    conn.execute("SELECT * FROM t WHERE m = " + merchant_id)
    # ruleid: fin-006-sql-string-building
    conn.execute("SELECT * FROM t WHERE m = '{}'".format(merchant_id))


def good(conn, merchant_id):
    # ok: fin-006-sql-string-building
    conn.execute("SELECT * FROM t WHERE m = ?", (merchant_id,))
