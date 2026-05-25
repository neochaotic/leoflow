from airflow._core import BaseOperator


class BashOperator(BaseOperator):
    """Name carries 'Bash' -> Leoflow 'bash'; reads .bash_command."""
