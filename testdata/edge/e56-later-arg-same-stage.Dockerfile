# Story 9.4's message defect, frozen.
#
# The literal being moved is on the ENV. LOWER DOWN in the same stage there is
# already an `ARG APP_VERSION` carrying exactly the same value — which is legal,
# common (a build stage that re-reads the version late), and says NOTHING about
# what is in scope at the ENV: an ARG declared after an instruction is not
# visible to it.
#
# argsInScope counted it anyway, so the plan reported "already declared", wrote
# no declaration, and the readback then refused the whole operation with
# "nothing declares APP_VERSION above instruction 1" — a refusal whose own
# reason was created by the step before it.
FROM alpine:3.20
ENV APP_VERSION=1.2.3
RUN echo "building ${APP_VERSION}"
ARG APP_VERSION=1.2.3
LABEL version="${APP_VERSION}"
