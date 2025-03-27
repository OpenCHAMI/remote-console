# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [2.4.0] - 2025-02-13
### Dependencies
- CASMCMS-9282: Bump Alpine version from 3.15 to 3.21, because 3.15 no longer receives security patches

## [2.3.0] - 2024-12-19
### Fixed
- CASMTRIAGE-7594 - clean up resilience.

## [2.2.0] - 2024-09-03
### Changed
- CASMPET-7065 - update to cray-services:11.0.0 base chart

## [2.1.0] - 2024-05-03
### Added
- CASMCMS-8899 - add support for Paradise (xd224) nodes.

### Changed
- Disabled concurrent Jenkins builds on same branch/commit
- Added build timeout to avoid hung builds

### Removed
- Removed defunct files leftover from previous versioning system

## [2.0.0] - 2023-03-30
### Changed
- CASMCMS-8456 - Update chart to use new postgres operator
- CASMCSM-7167 - Adding xname filtering so that console-node pods do not monitor the machines they are running on.
## [1.6.3] - 2022-02-24
### Changed
- CASMCMS-8423 - linting changes due to new version of gofmt.

## [1.6.2] - 2022-12-20
### Added
- Add Artifactory authentication to Jenkinsfile

### Changed
 - CASMCMS-8252: Enabled building of unstable artifacts
 - CASMCMS-8252: Updated header of update_versions.conf to reflect new tool options

### Fixed
 - CASMCMS-8156: Fix bug in handling Hill nodes.
 - Spelling corrections.
 - CASMCMS-8252: Update Chart with correct image and chart version strings during builds.

## [1.6.0] - 2022-08-04
### Changed
 - CASMINST-5145: Update the base service chart to pull in necessary changes for upgraded istio
 - CASMCMS-8140: Fix handling Hill nodes.

## [1.4.0] - 2022-07-12
### Changed
 - CASMCMS-7830: Update the base image to newer version.
