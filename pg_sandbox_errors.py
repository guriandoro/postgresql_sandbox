import sys
import os

# Error codes
ERR_GENERIC_ERROR = 1
ERR_INCORRECT_PARAM_COUNT = 2
ERR_INCORRECT_PARAMS = 3
ERR_INCORRECT_COMMAND = 4
ERR_SBOXDIR_EXISTS = 5
ERR_VAR_NOT_DEFINED = 6
ERR_SBOX_NOT_RUNNING = 7
ERR_SBOXDIR_NOT_EXISTS = 8
ERR_ROOTDIR_NOT_EXISTS = 9
ERR_SBOX_ALREADY_STOPPED = 10
ERR_PORT_IN_USE = 11
ERR_OUT_FILE_NOT_EXISTS = 12
ERR_SUBCOMMAND_NOT_SPECIFIED = 13
ERR_BUILD_FAILED = 14
ERR_BIN_NOT_FOUND = 15

# Error messages
# ERR_GENERIC_ERROR_MESSAGE = comes from generic exception
ERR_INCORRECT_PARAM_COUNT_MESSAGE = "At least one command should be used. Check usage with 'help' command."
# ERR_INCORRECT_PARAMS_MESSAGE = comes from getopts exception message
ERR_INCORRECT_COMMAND_MESSAGE = "Incorrect command. Check usage with 'help' command."
ERR_SBOXDIR_EXISTS_MESSAGE = "Sandbox directory already exists. Can't deploy to an existing directory."
# ERR_VAR_NOT_DEFINED_MESSAGE = will depend on each case
ERR_SBOX_NOT_RUNNING_MESSAGE = "Sandbox is not running."
ERR_SBOXDIR_NOT_EXISTS_MESSAGE = "Sandbox directory doesn't exist."
ERR_ROOTDIR_NOT_EXISTS_MESSAGE = "The PostgreSQL Sandbox root directory is needed to hold all sandboxes. Please create it.\nYou can modify it in the code, via the PGS_ROOT_DIR constant, if needed."
# ERR_SBOX_ALREADY_STOPPED_MESSAGE = no message needed
ERR_PORT_IN_USE_MESSAGE = "The chosen port is in use. Use another one with -p or --port."
ERR_OUT_FILE_NOT_EXISTS_MESSAGE = "The chosen out.txt file does not exist: "
ERR_SUBCOMMAND_NOT_SPECIFIED_MESSAGE = "Subcommand not specified. Check usage with 'help' command."
ERR_BUILD_FAILED_MESSAGE = "PostgreSQL build step failed."
ERR_BIN_NOT_FOUND_MESSAGE = "Required PostgreSQL binary could not be located. Make sure 'pg_sandbox build' has been run for the chosen version, or pass --bin to point to a valid binary directory."

# Functions
def print_and_exit(message):
    print(message)
    sys.exit(0)

def print_error_and_exit(err_code, err_message):
    print("ERROR:", err_message)
    sys.exit(err_code)

def print_error(err_code, err_message):
    print("ERROR:", err_message)

def print_debug(message, optional=None):
    debug = os.getenv("PGS_DEBUG")
    if (debug is not None) and (debug == "1"):
        if optional is None:
            print("#DEBUG: ", message)
        else:
            print("#DEBUG: ", message, optional)


def print_step_failure_and_exit(err_code, step_name, stderr_text=None, hint=None):
    """Print a build/subprocess step failure with optional stderr + hint, then exit.

    Centralizes the recurring 'print stderr + return appropriate code' pattern
    used by exec_build (and any other multi-step subprocess routine).
    """
    print("ERROR: '"+step_name+"' step failed.")
    if stderr_text:
        print(stderr_text)
    if hint:
        print(hint)
    sys.exit(err_code)
