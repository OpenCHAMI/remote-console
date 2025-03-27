# Console Data Service

The Console Data Service handles recording the current states of which nodes are being monitored by which pods.  The service is used by the Console Node and Console Operator projects exclusively to support the required operations of those projects.

## Related Software
Console Node Service - Responsible for handling direct console node connections, recording console logs, forwarding the log information to SMF and providing interactive console access.

Console Operator Service - Handles watching for hardware changes, watches for problems with running Console Node pods and scales the number of Console Node pods based on the number of nodes being monitored.

## Build and run the Docker image
The project leverages Docker Compose to simplify operations in development.  To build and start the service locally run:
````
./startDevServices.sh
````

To stop the running service run:
````
./stopDevServices.sh
````

## Testing
The service is integrated with PostgreSQL (deployed in production via the standard Helm service chart and operator).  For local testing the project leverages Docker Compose to setup the PostgresSQL dependency and execute the tests.
To build the latest service image from local source and execute the tests locally run:
````
./runIntegrationTest.sh
````

## Build Helpers
This repo uses some build helpers from the 
[cms-meta-tools](https://github.com/Cray-HPE/cms-meta-tools) repo. See that repo for more details.

## Local Builds
If you wish to perform a local build, you will first need to clone or copy the contents of the
cms-meta-tools repo to `./cms_meta_tools` in the same directory as the `Makefile`. When building
on github, the cloneCMSMetaTools() function clones the cms-meta-tools repo into that directory.

For a local build, you will also need to manually write the .version, .docker_version (if this repo
builds a docker image), and .chart_version (if this repo builds a helm chart) files. When building
on github, this is done by the setVersionFiles() function.

## Copyright and License
This project is copyrighted by Hewlett Packard Enterprise Development LP and is under the MIT license. See the LICENSE file for details.

When making any modifications to a file that has a Cray/HPE copyright header, that header must be updated to include the current year.

When creating any new files in this repo, if they contain source code, they must have the HPE copyright and license text in their header, unless the file is covered under someone else's copyright/license (in which case that should be in the header). For this purpose, source code files include Dockerfiles, Ansible files, RPM spec files, and shell scripts. It does not include Jenkinsfiles, OpenAPI/Swagger specs, or READMEs.

When in doubt, provided the file is not covered under someone else's copyright or license, then it does not hurt to add ours to the header.
