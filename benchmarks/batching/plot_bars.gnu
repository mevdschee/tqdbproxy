#!/usr/bin/gnuplot
set terminal pngcairo size 1600,600 enhanced font 'Arial,12'
set output 'batching_performance.png'

set multiplot layout 1,2 title "Hint-Based Write Batching Performance (3s tests, max 1k batch size)"

# Left plot - PostgreSQL
set title "PostgreSQL"
set xlabel "Batch Hint (ms)"
set ylabel "Throughput (k ops/sec)" textcolor rgb "blue"
set y2label "Latency (ms)" textcolor rgb "red"
set yrange [0:*]
set y2range [0:*]
set ytics nomirror
set y2tics
set style data histograms
set style histogram clustered gap 1
set style fill solid 0.5 border -1
set boxwidth 0.8
set grid y

plot 'bars_postgres.dat' using 2:xtic(1) title 'Throughput' axes x1y1 linecolor rgb "blue", \
     'bars_postgres.dat' using 3 title 'Latency' axes x1y2 linecolor rgb "red"

# Right plot - MariaDB
unset title
set title "MariaDB"
set xlabel "Batch Hint (ms)"
set ylabel "Throughput (k ops/sec)" textcolor rgb "blue"
set y2label "Latency (ms)" textcolor rgb "red"
set yrange [0:*]
set y2range [0:*]

plot 'bars_mysql.dat' using 2:xtic(1) title 'Throughput' axes x1y1 linecolor rgb "blue", \
     'bars_mysql.dat' using 3 title 'Latency' axes x1y2 linecolor rgb "red"

unset multiplot
