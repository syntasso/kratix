.PHONY: works promises

ARGS ?=

works:
	go run ./works.go $(ARGS) $(filter-out --,$(filter-out $@,$(MAKECMDGOALS)))

promises:
	go run ./promises.go $(ARGS) $(filter-out --,$(filter-out $@,$(MAKECMDGOALS)))
