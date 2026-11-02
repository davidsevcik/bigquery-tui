Output "/work/out/complete.gif"
Source "/vhs/common.tape"
Hide
Type "clear && cd /work && ./bqt" Enter
Sleep 2s
Ctrl+W
Type "l"
Sleep 300ms
Type "i"
Type "SELECT o.country, SUM(o.amount) AS revenue" Enter
Type "FROM `acme-prod.sales.orders` AS o" Enter
Type "WHERE o."
Sleep 800ms
Show
Sleep 300ms
Screenshot "/work/out/complete.png"
