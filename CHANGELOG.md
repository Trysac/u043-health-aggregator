# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Add the `healthaggregator` package: concurrent health checks with per-check timeouts, critical versus optional checks, up/degraded/down aggregation, TTL caching with a shared single-flight refresh, and an HTTP handler that hides internal error text by default.
- Initial project structure from the factory utility template.
