#!/usr/bin/gnuplot

set terminal png size 1600,900 enhanced font 'Arial,12'
set output 'adaptive_benchmark.png'

set title 'Adaptive Write Batching: Delay vs Throughput Under Different Loads' font 'Arial,16'
set xlabel 'Time (seconds)' font 'Arial,12'
set ylabel 'Delay (ms)' font 'Arial,12' textcolor rgb 'blue'
set y2label 'Throughput (ops/sec)' font 'Arial,12' textcolor rgb 'red'

set ytics nomirror tc rgb 'blue'
set y2tics nomirror tc rgb 'red'

set grid
set key outside right top

set style line 1 lc rgb '#0060ad' lt 1 lw 2 pt 7 ps 0.5
set style line 2 lc rgb '#dd181f' lt 1 lw 2 pt 5 ps 0.5
set style line 3 lc rgb '#00ad60' lt 1 lw 2 pt 9 ps 0.5
set style line 4 lc rgb '#ad6000' lt 1 lw 2 pt 11 ps 0.5

set datafile separator ","

plot 'adaptive_data.csv' using 6:($2==1000?$4:1/0) with linespoints ls 1 title '1k ops/s - Delay' axes x1y1, \
     'adaptive_data.csv' using 6:($2==1000?$3:1/0) with lines ls 1 dt 2 title '1k ops/s - Throughput' axes x1y2, \
     'adaptive_data.csv' using 6:($2==10000?$4:1/0) with linespoints ls 2 title '10k ops/s - Delay' axes x1y1, \
     'adaptive_data.csv' using 6:($2==10000?$3:1/0) with lines ls 2 dt 2 title '10k ops/s - Throughput' axes x1y2, \
     'adaptive_data.csv' using 6:($2==100000?$4:1/0) with linespoints ls 3 title '100k ops/s - Delay' axes x1y1, \
     'adaptive_data.csv' using 6:($2==100000?$3:1/0) with lines ls 3 dt 2 title '100k ops/s - Throughput' axes x1y2, \
     'adaptive_data.csv' using 6:($2==1000000?$4:1/0) with linespoints ls 4 title '1M ops/s - Delay' axes x1y1, \
     'adaptive_data.csv' using 6:($2==1000000?$3:1/0) with lines ls 4 dt 2 title '1M ops/s - Throughput' axes x1y2
