class SimpleClass:
    def __init__(self, value):
        self.value = value

    def get_value(self):
        return self.value


def main():
    obj = SimpleClass("hello")
    print(obj.get_value())
