Output "/work/out/run.gif"
Source "/vhs/common.tape"
Hide
Type "clear && cd /work && ./bqt" Enter
Sleep 2s
Ctrl+W
Type "l"
Sleep 300ms
Type "i"
Type "-- delivered revenue by day and country" Enter
Type "SELECT DATE(o.created_at) AS day, o.country," Enter
Type "COUNT(*) AS orders, SUM(o.amount) AS revenue" Enter
Type "FROM `acme-prod.sales.orders` AS o" Enter
Type "WHERE o.status = 'delivered'" Enter
Type "GROUP BY day, o.country" Enter
Type "ORDER BY day DESC, revenue DESC"
Escape
Sleep 300ms
Escape
Sleep 300ms
Enter
Sleep 4s
Show
Sleep 300ms
Screenshot "/work/out/run.png"
