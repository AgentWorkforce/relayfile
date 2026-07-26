#!/usr/bin/env ruby
# frozen_string_literal: true

require "fileutils"

# Print the supported probe roles and stop on malformed arguments.
def usage!
  warn <<~USAGE
    usage:
      measure-mount-latency.rb echo DIR PREFIX COUNT TIMEOUT_SECONDS
      measure-mount-latency.rb roundtrip DIR PREFIX COUNT INTERVAL_MS OUTPUT_CSV TIMEOUT_SECONDS

    PREFIX must be unique for the run. The initiator measures ping-to-ack time
    with its own monotonic clock, so separate-machine wall-clock skew is absent.
  USAGE
  exit 2
end

# Return nanoseconds from the current process's monotonic clock.
def monotonic_ns
  Process.clock_gettime(Process::CLOCK_MONOTONIC, :nanosecond)
end

# Publish a probe without exposing a partially written file to the mount.
def atomic_write(path, body)
  temporary_path = "#{path}.tmp-#{Process.pid}"
  File.write(temporary_path, body)
  File.rename(temporary_path, path)
end

# Poll a condition until it succeeds or the monotonic deadline expires.
def wait_until(deadline)
  loop do
    return if yield

    if Process.clock_gettime(Process::CLOCK_MONOTONIC) >= deadline
      raise Timeout::Error
    end
    sleep 0.005
  end
end

# Require an explicit run identifier whose probe namespace is still empty.
def validate_fresh_prefix!(directory, prefix)
  unless prefix.match?(/\A[a-zA-Z0-9][a-zA-Z0-9_.-]*\z/)
    warn "PREFIX must contain only letters, digits, dots, underscores, and hyphens"
    exit 2
  end

  stale = Dir.glob(File.join(directory, "#{prefix}-*.{ping,ack}"))
  return if stale.empty?

  warn "refusing non-unique PREFIX #{prefix.inspect}: found #{stale.length} existing probe files"
  exit 1
end

mode = ARGV.shift

case mode
when "echo"
  directory, prefix, count_text, timeout_text = ARGV
  usage! unless directory && prefix && count_text && timeout_text

  require "timeout"
  count = Integer(count_text, 10)
  deadline = Process.clock_gettime(Process::CLOCK_MONOTONIC) + Float(timeout_text)
  seen_sequences = {}
  FileUtils.mkdir_p(directory)
  validate_fresh_prefix!(directory, prefix)

  begin
    until seen_sequences.length == count
      Dir.glob(File.join(directory, "#{prefix}-*.ping")).sort.each do |path|
        observed_prefix, sequence_text = File.read(path).strip.split(",", 2)
        next unless observed_prefix == prefix

        sequence = Integer(sequence_text, 10)
        next unless sequence.between?(1, count)
        next if seen_sequences[sequence]

        atomic_write(
          File.join(directory, format("%s-%03d.ack", prefix, sequence)),
          "#{prefix},#{sequence}\n",
        )
        seen_sequences[sequence] = true
      rescue ArgumentError, Errno::ENOENT
        # Atomic renames and arriving mount files can race the directory scan.
      end

      break if seen_sequences.length == count
      raise Timeout::Error if Process.clock_gettime(Process::CLOCK_MONOTONIC) >= deadline

      sleep 0.005
    end
  rescue Timeout::Error
    warn "timed out after acknowledging #{seen_sequences.length}/#{count} #{prefix} probes"
    exit 1
  end
when "roundtrip"
  directory, prefix, count_text, interval_text, output_path, timeout_text = ARGV
  usage! unless directory && prefix && count_text && interval_text && output_path && timeout_text

  require "timeout"
  count = Integer(count_text, 10)
  interval = Float(interval_text) / 1_000
  timeout = Float(timeout_text)
  FileUtils.mkdir_p(directory)
  FileUtils.mkdir_p(File.dirname(output_path))
  validate_fresh_prefix!(directory, prefix)

  File.open(output_path, "w") do |output|
    output.puts("prefix,sequence,round_trip_ms")
    output.flush

    count.times do |index|
      sequence = index + 1
      ping_path = File.join(directory, format("%s-%03d.ping", prefix, sequence))
      ack_path = File.join(directory, format("%s-%03d.ack", prefix, sequence))
      expected = "#{prefix},#{sequence}"
      sent_ns = monotonic_ns
      atomic_write(ping_path, "#{expected}\n")
      deadline = Process.clock_gettime(Process::CLOCK_MONOTONIC) + timeout

      begin
        wait_until(deadline) do
          File.file?(ack_path) && File.read(ack_path).strip == expected
        rescue Errno::ENOENT
          false
        end
      rescue Timeout::Error
        warn "timed out waiting for acknowledgment #{sequence}/#{count} for #{prefix}"
        exit 1
      end

      received_ns = monotonic_ns
      round_trip_ms = (received_ns - sent_ns) / 1_000_000.0
      output.puts(format("%s,%d,%.3f", prefix, sequence, round_trip_ms))
      output.flush
      sleep interval if sequence < count
    end
  end
else
  usage!
end
