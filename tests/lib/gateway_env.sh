# shellcheck shell=bash
# Source this before starting any ClawEh gateway outside production, so a
# test gateway never alerts the operator.
#
# The alerter (github.com/tenebris-tech/alerter) loads ~/.alerter, which holds
# the operator's real Pushover, SMS, SMTP and webhook settings, into every
# gateway, but only for variables not already in the environment. A channel
# whose variables are all empty is not created, so exporting them empty keeps
# delivery off and alerts land only in <CLAW_HOME>/logs/alerts.log. Every
# variable that switches a channel on is listed: one left to ~/.alerter would
# either deliver or leave a channel half-configured, which disables alerting
# altogether. ALERTER_LOG is emptied so the alerts log stays in CLAW_HOME.
#
# Safe to source more than once.
export ALERTER_PUSHOVER_TOKEN= ALERTER_PUSHOVER_DEST= \
    ALERTER_PUSHOVER_NORMAL= ALERTER_PUSHOVER_URGENT= ALERTER_PUSHOVER_EMERGENCY= \
    ALERTER_TELNYX_API_KEY= ALERTER_SMS_FROM= ALERTER_SMS_TO= \
    ALERTER_SMTP_HOST= ALERTER_SMTP_PORT= ALERTER_SMTP_USER= ALERTER_SMTP_PASSWORD= \
    ALERTER_SMTP_FROM= ALERTER_SMTP_TO= \
    ALERTER_WEBHOOK_URL= ALERTER_WEBHOOK_HEADERS= \
    ALERTER_LOG=
