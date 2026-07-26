#!/usr/bin/env ruby
# frozen_string_literal: true

require "fileutils"

def usage!
  warn <<~USAGE
    usage:
      measure-mount-latency.rb write DIR PREFIX COUNT INTERVAL_MS
      measure-mount-latency.rb receive DIR PREFIX COUNT OUTPUT_CSV TIMEOUT_SECONDS
  USAGE
  exit 2
end

def realtime_ns
  Process.clock_gettime(Process::CLOCK_REALTIME, :nanosecond)
end

mode = ARGV.shift

case mode
when "write"
  directory, prefix, count_text, interval_text = ARGV
  usage! unless directory && prefix && count_text && interval_text

  count = Integer(count_text, 10)
  interval = Float(interval_text) / 1_000
  FileUtils.mkdir_p(directory)

  count.times do |index|
    sequence = index + 1
    sent_ns = realtime_ns
    final_path = File.join(directory, format("%s-%03d.probe", prefix, sequence))
    temporary_path = "#{final_path}.tmp-#{Process.pid}"
    File.write(temporary_path, "#{prefix},#{sequence},#{sent_ns}\n")
    File.rename(temporary_path, final_path)
    sleep interval if sequence < count
  end
when "receive"
  directory, prefix, count_text, output_path, timeout_text = ARGV
  usage! unless directory && prefix && count_text && output_path && timeout_text

  count = Integer(count_text, 10)
  deadline = Process.clock_gettime(Process::CLOCK_MONOTONIC) + Float(timeout_text)
  seen = {}
  FileUtils.mkdir_p(File.dirname(output_path))

  File.open(output_path, "w") do |output|
    output.puts("prefix,sequence,sent_ns,received_ns,latency_ms")
    output.flush

    until seen.length == count
      Dir.glob(File.join(directory, "#{prefix}-*.probe")).sort.each do |path|
        next if seen[path]

        observed = File.read(path).strip.split(",", 3)
        next unless observed.length == 3 && observed[0] == prefix

        received_ns = realtime_ns
        sequence = Integer(observed[1], 10)
        sent_ns = Integer(observed[2], 10)
        latency_ms = (received_ns - sent_ns) / 1_000_000.0
        output.puts(format("%s,%d,%d,%d,%.3f", prefix, sequence, sent_ns, received_ns, latency_ms))
        output.flush
        seen[path] = true
      rescue ArgumentError, Errno::ENOENT
        # An atomic rename or a just-arriving file can race the directory scan.
        # Retry on the next 5 ms pass.
      end

      if Process.clock_gettime(Process::CLOCK_MONOTONIC) >= deadline
        warn "timed out after observing #{seen.length}/#{count} #{prefix} probes"
        exit 1
      end
      sleep 0.005
    end
  end
else
  usage!
end
