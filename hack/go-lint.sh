#!/bin/sh


go run ./vendor/golang.org/x/lint/golint -set_exit_status "${@}"

